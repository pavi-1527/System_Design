package shorty

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
)

// runStore is the store role: open (and recover) the log, then serve it.
// Provided.
func runStore(ctx context.Context, cfg StoreConfig, stdout io.Writer) error {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}
	lg, rec, err := OpenLog(filepath.Join(cfg.DataDir, "store.log"), cfg.Sync)
	if err != nil {
		return err
	}
	defer lg.Close()
	s := &storeServer{log: lg, rec: rec}
	return serve(ctx, cfg.Addr, s.routes(), cfg.Drain, stdout)
}

// storeServer exposes a Log over HTTP (spec.md "Store API"):
//
//	GET    /kv/{key...}               200 value | 404
//	PUT    /kv/{key...}               204
//	PUT    /kv/{key...}?if-absent=1   201 inserted | 409 with the existing value as the body
//	DELETE /kv/{key...}               204
//	GET    /-/stats                   200 {"keys","reads","writes","conflicts","recovered_records","truncated_bytes"}
//	GET    /-/healthz                 200
//
// Bad keys or oversized values are 400; a failed write is 500. "reads" counts
// every GET /kv request, found or not: the API tests read it to see how many
// lookups actually reached the store.
type storeServer struct {
	log       *Log
	rec       Recovery
	reads     atomic.Int64
	writes    atomic.Int64
	conflicts atomic.Int64
}

func (s *storeServer) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /kv/{key...}", s.get)
	mux.HandleFunc("PUT /kv/{key...}", s.put)
	mux.HandleFunc("DELETE /kv/{key...}", s.del)
	mux.HandleFunc("GET /-/stats", s.stats)
	mux.HandleFunc("GET /-/healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok\n") })
	return mux
}

func (s *storeServer) get(w http.ResponseWriter, r *http.Request) {
	s.reads.Add(1)
	v, ok := s.log.Get(r.PathValue("key"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(v)
}

func (s *storeServer) put(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	val, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxValueLen))
	if err != nil {
		http.Error(w, "value too large", http.StatusBadRequest)
		return
	}
	if err := checkKV(key, val); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("if-absent") == "1" {
		inserted, existing, err := s.log.PutIfAbsent(key, val)
		switch {
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		case inserted:
			s.writes.Add(1)
			w.WriteHeader(http.StatusCreated)
		default:
			s.conflicts.Add(1)
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusConflict)
			w.Write(existing)
		}
		return
	}
	if err := s.log.Put(key, val); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writes.Add(1)
	w.WriteHeader(http.StatusNoContent)
}

func (s *storeServer) del(w http.ResponseWriter, r *http.Request) {
	if err := s.log.Delete(r.PathValue("key")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writes.Add(1)
	w.WriteHeader(http.StatusNoContent)
}

func (s *storeServer) stats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]int64{
		"keys":              int64(s.log.Len()),
		"reads":             s.reads.Load(),
		"writes":            s.writes.Load(),
		"conflicts":         s.conflicts.Load(),
		"recovered_records": int64(s.rec.Records),
		"truncated_bytes":   s.rec.TruncatedBytes,
	})
}

// writeJSON writes v as a JSON response. Provided.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

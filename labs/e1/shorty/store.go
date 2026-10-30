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
// lookups actually reached the store. Keys contain '/', hence {key...}.
type storeServer struct {
	log       *Log
	rec       Recovery
	reads     atomic.Int64
	writes    atomic.Int64
	conflicts atomic.Int64
}

func (s *storeServer) routes() *http.ServeMux {
	mux := http.NewServeMux()
	// TODO(lab): register the six routes above (Go 1.22 patterns such as
	// "GET /kv/{key...}" and r.PathValue("key")). Read PUT bodies through
	// http.MaxBytesReader(w, r.Body, MaxValueLen).
	return mux
}

// writeJSON writes v as a JSON response. Provided.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

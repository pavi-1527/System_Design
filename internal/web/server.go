// Package web is the local browser front end of the course (`sd serve`): a JSON API
// over the same packages the CLI uses (course, progress, mastery, quiz, review,
// estimate, labrun) plus an embedded single-page app. Every request re-reads
// course metadata and progress.json from disk, so modules written by other sessions
// appear without a restart and the CLI and the browser can be used side by side.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chethu007/system-design-course/internal/course"
	"github.com/chethu007/system-design-course/internal/labrun"
	"github.com/chethu007/system-design-course/internal/progress"
)

//go:embed static
var staticFS embed.FS

// Config says where the course lives and how to tell the time.
type Config struct {
	Root         string           // course root (directory containing course/course.json)
	ProgressPath string           // progress.json
	Now          func() time.Time // nil = time.Now
}

// Server holds the API's in-memory state: open quiz/review/estimation sessions.
// Everything durable lives in progress.json.
type Server struct {
	cfg Config

	mu       sync.Mutex // serializes progress.json read-modify-write and session access
	sessions map[string]*session
	rng      *rand.Rand

	labMu     sync.Mutex // one `go test` at a time
	raceOnce  sync.Once
	raceAvail bool
}

// New returns a server for cfg.
func New(cfg Config) *Server {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	seed := uint64(time.Now().UnixNano())
	return &Server{cfg: cfg, sessions: map[string]*session{}, rng: rand.New(rand.NewPCG(seed, 0x5e7e))}
}

// Handler returns the HTTP handler: the API under /api/, embedded assets under
// /static/, and the single-page app for every other path.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/modules/{id}", s.handleModule)
	mux.HandleFunc("GET /api/doc", s.handleDoc)
	mux.HandleFunc("PUT /api/predictions/{pid}", s.handlePredict)
	mux.HandleFunc("GET /api/progress", s.handleProgress)
	mux.HandleFunc("POST /api/sessions", s.handleSessionStart)
	mux.HandleFunc("POST /api/sessions/{sid}/answers", s.handleSessionAnswer)
	mux.HandleFunc("POST /api/sessions/{sid}/finish", s.handleSessionFinish)
	mux.HandleFunc("POST /api/estimate", s.handleEstimateStart)
	mux.HandleFunc("POST /api/estimate/{sid}/answer", s.handleEstimateAnswer)
	mux.HandleFunc("GET /api/labs/{id}", s.handleLabs)
	mux.HandleFunc("POST /api/labs/{id}/run", s.handleLabRunAll)
	mux.HandleFunc("POST /api/labs/{id}/{pkg}/run", s.handleLabRun)
	mux.HandleFunc("GET /api/labs/{id}/{pkg}/solution", s.handleSolution)

	static, _ := fs.Sub(staticFS, "static")
	files := http.FileServer(http.FS(static))
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheHeaders(files)))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/"):
			writeErr(w, http.StatusNotFound, "no such API endpoint")
		case r.Method != http.MethodGet && r.Method != http.MethodHead:
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		default:
			// Client-side routes (/module/A0, /doc/..., /quiz/A0, ...) all load the app shell.
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFileFS(w, r, static, "index.html")
		}
	})
	return guard(mux)
}

// cacheHeaders lets the browser keep the large vendored libraries and revalidate the
// app's own files (embedded files carry no modification time to validate against).
func cacheHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "vendor/") {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		h.ServeHTTP(w, r)
	})
}

// guard rejects requests that didn't come from a page served by this server: a Host
// header that isn't loopback (DNS rebinding) or, for writes, a foreign Origin or a
// non-JSON body (cross-site form posts). The API can run `go test`, so this matters
// even on localhost.
func guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			writeErr(w, http.StatusForbidden, "sd serve only answers on localhost")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" {
				u, err := url.Parse(o)
				if err != nil || u.Host != r.Host {
					writeErr(w, http.StatusForbidden, "cross-origin request refused")
					return
				}
			}
			if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				writeErr(w, http.StatusUnsupportedMediaType, "send JSON (Content-Type: application/json)")
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ListenAndServe serves on 127.0.0.1:port until ctx is cancelled. ready, if non-nil,
// is called with the URL once the listener is open.
func (s *Server) ListenAndServe(ctx context.Context, port int, ready func(url string)) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	if ready != nil {
		ready(fmt.Sprintf("http://localhost:%d", ln.Addr().(*net.TCPAddr).Port))
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shut); err != nil {
			return err
		}
		if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

// state is one consistent read of the course and the learner's progress.
type state struct {
	c *course.Course
	p *progress.Progress
}

func (s *Server) load() (*state, error) {
	c, err := course.Load(s.cfg.Root)
	if err != nil {
		return nil, err
	}
	p, err := progress.Load(s.cfg.ProgressPath)
	if err != nil {
		return nil, err
	}
	return &state{c: c, p: p}, nil
}

func (s *Server) save(st *state) error { return st.p.Save(s.cfg.ProgressPath) }

func (s *Server) now() time.Time { return s.cfg.Now() }

func (s *Server) raceAvailable(ctx context.Context) bool {
	s.raceOnce.Do(func() { s.raceAvail = labrun.RaceAvailable(ctx) })
	return s.raceAvail
}

// apiError carries an HTTP status through helpers.
type apiError struct {
	code int
	msg  string
}

func (e *apiError) Error() string { return e.msg }

func errorf(code int, format string, a ...any) error {
	return &apiError{code: code, msg: fmt.Sprintf(format, a...)}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// reply writes v, or the error with its status (500 for unexpected errors).
func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) {
			writeErr(w, ae.code, ae.msg)
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func decode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return errorf(http.StatusBadRequest, "bad JSON body: %v", err)
	}
	return nil
}

func moduleOf(c *course.Course, id string) (*course.Module, error) {
	m, err := c.Module(id)
	if err != nil {
		return nil, errorf(http.StatusNotFound, "%v", err)
	}
	return m, nil
}

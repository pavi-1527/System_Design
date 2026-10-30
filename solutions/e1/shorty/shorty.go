// Package shorty is the E1 project: a URL shortener built as separate
// processes on 127.0.0.1, the way it would run in production minus the
// machines.
//
//	client ──HTTP──▶ api (any number of instances) ──HTTP──▶ store (one)
//	                 cache · rate limiter · budgets           append-only log
//
// One binary, two roles, chosen by the first argument:
//
//	shorty store -addr 127.0.0.1:7001 -data ./data -sync always
//	shorty api   -addr 127.0.0.1:8081 -store http://127.0.0.1:7001
//
// Both roles print "READY <host:port>" on stdout once they are listening, refuse
// to bind anything but a loopback address, and drain gracefully on SIGTERM. The
// full contract (flags, endpoints, status codes, on-disk format) is in
// modules/E1-url-shortener-service/spec.md; the tests in this package are its
// executable form. Run them by milestone:
//
//	go test -race -run TestM1 ./labs/e1/shorty/   # the log and the store process
//	go test -race -run TestM2 ./labs/e1/shorty/   # the API and the read cache
//	go test -race -run TestM3 ./labs/e1/shorty/   # the rate limiter
//	go test -race -run TestM4 ./labs/e1/shorty/   # failure injection
package shorty

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// errBadConfig marks configuration errors: Run exits 2 for them, 1 otherwise.
var errBadConfig = errors.New("bad configuration")

// Run is the process entry point shared by cmd/shorty and the tests. args[0]
// is the role ("store" or "api"); the rest are that role's flags. It returns
// the process exit code: 0 after a clean shutdown (ctx cancelled), 2 for usage
// or configuration errors, 1 for anything else.
//
// Provided (not part of the exercise).
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: shorty store|api [flags]")
		return 2
	}
	role, rest := args[0], args[1:]
	var err error
	switch role {
	case "store":
		var cfg StoreConfig
		if cfg, err = parseStoreFlags(rest, stderr); err == nil {
			err = runStore(ctx, cfg, stdout)
		}
	case "api":
		var cfg APIConfig
		if cfg, err = parseAPIFlags(rest, stderr); err == nil {
			err = runAPI(ctx, cfg, stdout)
		}
	default:
		fmt.Fprintf(stderr, "shorty: unknown role %q (want store or api)\n", role)
		return 2
	}
	if err == nil {
		return 0
	}
	fmt.Fprintf(stderr, "shorty %s: %v\n", role, err)
	if errors.Is(err, errBadConfig) {
		return 2
	}
	return 1
}

// StoreConfig is the store role's configuration. Provided.
type StoreConfig struct {
	Addr    string        // listen address; loopback only
	DataDir string        // the log lives at DataDir/store.log
	Sync    SyncPolicy    // when to fsync
	Drain   time.Duration // how long SIGTERM waits for in-flight requests
}

// APIConfig is the api role's configuration. Provided.
type APIConfig struct {
	Addr           string        // listen address; loopback only
	StoreURL       string        // base URL of the store, e.g. http://127.0.0.1:7001
	StoreTimeout   time.Duration // cap on any single store call
	RequestBudget  time.Duration // cap on all the store calls one API request makes together
	CacheSize      int           // max entries in the redirect cache
	CacheTTL       time.Duration // how long a found/gone entry is fresh
	NegativeTTL    time.Duration // how long a not-found entry is fresh
	StaleIfError   time.Duration // how long past freshness an entry may be served while the store is failing
	RedirectMaxAge time.Duration // 0: redirects are "private, no-store"; >0: "private, max-age=N"
	CreateRate     float64       // creates per second per client (token refill rate)
	CreateBurst    int           // token bucket size per client
	MaxClients     int           // bound on rate-limiter memory (distinct client keys)
	CodeLen        int           // length of generated codes
	Drain          time.Duration // how long SIGTERM waits for in-flight requests
}

func parseStoreFlags(args []string, stderr io.Writer) (StoreConfig, error) {
	var cfg StoreConfig
	fs := flag.NewFlagSet("shorty store", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.Addr, "addr", "127.0.0.1:7001", "listen address (loopback only)")
	fs.StringVar(&cfg.DataDir, "data", "./data", "directory for store.log")
	syncFlag := fs.String("sync", "always", "fsync policy: always | never")
	fs.DurationVar(&cfg.Drain, "drain", 5*time.Second, "graceful shutdown drain timeout")
	if err := fs.Parse(args); err != nil {
		return cfg, fmt.Errorf("%w: %v", errBadConfig, err)
	}
	if err := checkLoopback(cfg.Addr); err != nil {
		return cfg, err
	}
	p, err := ParseSyncPolicy(*syncFlag)
	if err != nil {
		return cfg, fmt.Errorf("%w: %v", errBadConfig, err)
	}
	cfg.Sync = p
	return cfg, nil
}

func parseAPIFlags(args []string, stderr io.Writer) (APIConfig, error) {
	var cfg APIConfig
	fs := flag.NewFlagSet("shorty api", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.Addr, "addr", "127.0.0.1:8081", "listen address (loopback only)")
	fs.StringVar(&cfg.StoreURL, "store", "http://127.0.0.1:7001", "store base URL (loopback only)")
	fs.DurationVar(&cfg.StoreTimeout, "store-timeout", 300*time.Millisecond, "cap on one store call")
	fs.DurationVar(&cfg.RequestBudget, "request-budget", time.Second, "cap on all store calls of one request")
	fs.IntVar(&cfg.CacheSize, "cache-size", 10000, "redirect cache entries")
	fs.DurationVar(&cfg.CacheTTL, "cache-ttl", 30*time.Second, "freshness of found/gone entries")
	fs.DurationVar(&cfg.NegativeTTL, "neg-ttl", 5*time.Second, "freshness of not-found entries")
	fs.DurationVar(&cfg.StaleIfError, "stale-if-error", 5*time.Minute, "serve stale entries this long while the store fails")
	fs.DurationVar(&cfg.RedirectMaxAge, "redirect-max-age", 0, "browser caching of redirects (0 = no-store)")
	fs.Float64Var(&cfg.CreateRate, "create-rate", 5, "creates per second per client")
	fs.IntVar(&cfg.CreateBurst, "create-burst", 20, "create burst per client")
	fs.IntVar(&cfg.MaxClients, "max-clients", 10000, "distinct clients tracked by the limiter")
	fs.IntVar(&cfg.CodeLen, "code-len", 7, "generated code length (4-16)")
	fs.DurationVar(&cfg.Drain, "drain", 5*time.Second, "graceful shutdown drain timeout")
	if err := fs.Parse(args); err != nil {
		return cfg, fmt.Errorf("%w: %v", errBadConfig, err)
	}
	if err := checkLoopback(cfg.Addr); err != nil {
		return cfg, err
	}
	u, err := url.Parse(cfg.StoreURL)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return cfg, fmt.Errorf("%w: -store must be an http:// URL, got %q", errBadConfig, cfg.StoreURL)
	}
	if err := checkLoopback(u.Host); err != nil {
		return cfg, err
	}
	switch {
	case cfg.CodeLen < 4 || cfg.CodeLen > 16:
		return cfg, fmt.Errorf("%w: -code-len must be 4-16", errBadConfig)
	case cfg.CacheSize < 1 || cfg.CreateBurst < 1 || cfg.MaxClients < 1:
		return cfg, fmt.Errorf("%w: -cache-size, -create-burst and -max-clients must be ≥ 1", errBadConfig)
	case cfg.CreateRate <= 0 || cfg.StoreTimeout <= 0 || cfg.RequestBudget <= 0:
		return cfg, fmt.Errorf("%w: -create-rate, -store-timeout and -request-budget must be > 0", errBadConfig)
	}
	return cfg, nil
}

// checkLoopback refuses any listen or dial address whose host is not a
// loopback IP or "localhost". These processes have no authentication; binding
// 0.0.0.0 would publish them to every network the laptop joins. Provided.
func checkLoopback(hostport string) error {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return fmt.Errorf("%w: address %q: %v", errBadConfig, hostport, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("%w: refusing non-loopback address %q (use 127.0.0.1)", errBadConfig, hostport)
}

// serve listens on addr, prints "READY <host:port>" to stdout, serves h until
// ctx is cancelled, then drains: it stops accepting, lets in-flight requests
// finish for up to drain, and returns. A drain that times out is an error
// (exit 1) because requests were cut off. Provided.
func serve(ctx context.Context, addr string, h http.Handler, drain time.Duration, stdout io.Writer) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	fmt.Fprintf(stdout, "READY %s\n", ln.Addr())
	select {
	case <-ctx.Done():
	case err := <-errc:
		return err
	}
	sctx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		_ = srv.Close()
		return fmt.Errorf("drain: %w", err)
	}
	return nil
}

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// NewCode returns n random base-62 characters from crypto/rand. Bytes ≥ 248
// are rejected so every character is equally likely (248 = 4 × 62). Random
// codes need no coordination between API instances; the store's
// insert-if-absent catches the rare collision. Provided (C1 Unit 1 §5 is
// where the alternatives live).
func NewCode(n int) string {
	out := make([]byte, 0, n)
	buf := make([]byte, 2*n)
	for len(out) < n {
		_, _ = rand.Read(buf)
		for _, b := range buf {
			if b < 248 {
				out = append(out, base62[b%62])
				if len(out) == n {
					break
				}
			}
		}
	}
	return string(out)
}

// validCode reports whether s could be a code at all, so garbage never costs a
// cache entry or a store read. Provided.
func validCode(s string) bool {
	if len(s) < 4 || len(s) > 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('0' <= c && c <= '9' || 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z') {
			return false
		}
	}
	return true
}

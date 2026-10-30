package shorty

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

// ErrStoreUnavailable wraps every failure that says nothing about the data:
// connection refused, a timeout, a reset, a 5xx. The API maps it to 503 with
// Retry-After (or serves a stale cache entry for redirects). Provided.
var ErrStoreUnavailable = errors.New("store unavailable")

// StoreClient talks to the store role over HTTP. Every method takes a context
// and must return as soon as it is done: the caller's deadline is the budget.
type StoreClient struct {
	base string
	hc   *http.Client
}

// NewStoreClient returns a client for the store at base (e.g.
// "http://127.0.0.1:7001"). The transport keeps a pool of keep-alive
// connections (one TCP handshake per connection, not per call) and fails a
// connect after 500 ms; there is deliberately no Client.Timeout, because the
// per-call deadline comes from the context. Provided.
func NewStoreClient(base string) *StoreClient {
	tr := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 500 * time.Millisecond}).DialContext,
		MaxIdleConns:        256,
		MaxIdleConnsPerHost: 256,
		IdleConnTimeout:     90 * time.Second,
	}
	return &StoreClient{base: strings.TrimRight(base, "/"), hc: &http.Client{Transport: tr}}
}

// Get returns the value for key; found is false on 404. Transport errors and
// 5xx wrap ErrStoreUnavailable.
func (c *StoreClient) Get(ctx context.Context, key string) (val []byte, found bool, err error) {
	// TODO(lab): http.NewRequestWithContext(ctx, …, c.base+"/kv/"+key, …);
	// always read and close the body (or the connection can't be reused).
	return nil, false, errNotImplemented
}

// Put sets key to val (expects 204).
func (c *StoreClient) Put(ctx context.Context, key string, val []byte) error {
	// TODO(lab)
	return errNotImplemented
}

// PutIfAbsent inserts key only if it is absent (?if-absent=1). When it is
// present, inserted is false and existing holds the current value (the 409
// body).
func (c *StoreClient) PutIfAbsent(ctx context.Context, key string, val []byte) (inserted bool, existing []byte, err error) {
	// TODO(lab)
	return false, nil, errNotImplemented
}

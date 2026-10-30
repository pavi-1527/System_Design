package shorty

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// ErrStoreUnavailable wraps every failure that says nothing about the data:
// connection refused, a timeout, a reset, a 5xx. The API maps it to 503 with
// Retry-After (or serves a stale cache entry for redirects).
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

func (c *StoreClient) do(ctx context.Context, method, key, query string, body []byte) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/kv/"+key+query, rd)
	if err != nil {
		return 0, nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxValueLen+1))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: reading response: %v", ErrStoreUnavailable, err)
	}
	if resp.StatusCode >= 500 {
		return 0, nil, fmt.Errorf("%w: %s %s: %d %s", ErrStoreUnavailable, method, key, resp.StatusCode, bytes.TrimSpace(b))
	}
	return resp.StatusCode, b, nil
}

// Get returns the value for key; found is false on 404.
func (c *StoreClient) Get(ctx context.Context, key string) (val []byte, found bool, err error) {
	code, b, err := c.do(ctx, http.MethodGet, key, "", nil)
	switch {
	case err != nil:
		return nil, false, err
	case code == http.StatusOK:
		return b, true, nil
	case code == http.StatusNotFound:
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("store GET %s: unexpected status %d", key, code)
}

// Put sets key to val.
func (c *StoreClient) Put(ctx context.Context, key string, val []byte) error {
	code, _, err := c.do(ctx, http.MethodPut, key, "", val)
	if err != nil {
		return err
	}
	if code != http.StatusNoContent {
		return fmt.Errorf("store PUT %s: unexpected status %d", key, code)
	}
	return nil
}

// PutIfAbsent inserts key only if it is absent. When it is present, inserted
// is false and existing holds the current value.
func (c *StoreClient) PutIfAbsent(ctx context.Context, key string, val []byte) (inserted bool, existing []byte, err error) {
	code, b, err := c.do(ctx, http.MethodPut, key, "?if-absent=1", val)
	switch {
	case err != nil:
		return false, nil, err
	case code == http.StatusCreated:
		return true, nil, nil
	case code == http.StatusConflict:
		return false, b, nil
	}
	return false, nil, fmt.Errorf("store PUT %s if-absent: unexpected status %d", key, code)
}

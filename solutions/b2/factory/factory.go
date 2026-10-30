// Package factory is a provider registry: the idiomatic Go factory.
//
// Lab B2 Unit 1 §4. A credit-union platform supports several cheque-image
// vendors, and each credit union picks one in its configuration. The
// anti-pattern is `switch cfg.Vendor { case "acme": ... }` in the code that
// uses the vendor, so adding a vendor edits every call site. The pattern: a
// registry maps a name to a constructor; configuration names a provider; code
// that needs one asks the registry and gets the interface. A new vendor is a
// new file that registers itself.
//
// Go has no classes, so the "factory" is a map of constructor functions. This
// is exactly the shape of database/sql's driver registry.
package factory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Provider is the consumer-side interface: all the rest of the system knows.
type Provider interface {
	Name() string
	Fetch(ctx context.Context, id string) ([]byte, error)
}

// Config is a provider's settings (endpoint, credentials reference, ...).
type Config map[string]string

// Constructor builds a Provider from its Config.
type Constructor func(cfg Config) (Provider, error)

var (
	// ErrDuplicate: a name was registered twice.
	ErrDuplicate = errors.New("factory: provider already registered")
	// ErrInvalid: an empty name or nil constructor.
	ErrInvalid = errors.New("factory: invalid registration")
	// ErrUnknown: no provider is registered under the requested name.
	ErrUnknown = errors.New("factory: unknown provider")
	// ErrNilProvider: a constructor returned (nil, nil).
	ErrNilProvider = errors.New("factory: constructor returned no provider")
)

// Registry maps provider names to constructors. Safe for concurrent use.
type Registry struct {
	mu    sync.RWMutex
	ctors map[string]Constructor
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{ctors: map[string]Constructor{}} }

// Register adds a constructor. An empty name or nil constructor is ErrInvalid;
// a name that is already taken is ErrDuplicate (wrapped with the name). Unlike
// a fee schedule, a provider name is an identity: silently replacing it would
// change which vendor a tenant talks to.
func (r *Registry) Register(name string, c Constructor) error {
	if name == "" || c == nil {
		return fmt.Errorf("%w: name %q, nil constructor %v", ErrInvalid, name, c == nil)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.ctors[name]; ok {
		return fmt.Errorf("%w: %q", ErrDuplicate, name)
	}
	r.ctors[name] = c
	return nil
}

// Names returns the registered provider names, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.ctors))
	for n := range r.ctors {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// New builds the provider registered as name.
//
// Contract: an unknown name is ErrUnknown, and the message names the
// requested provider and lists the known ones. The constructor is called with
// a COPY of cfg and WITHOUT the registry lock held, so it may itself call
// New (a failover provider built from two others). A constructor error is
// wrapped with the name; a constructor panic is recovered into an error; a
// (nil, nil) result is ErrNilProvider.
func (r *Registry) New(name string, cfg Config) (p Provider, err error) {
	r.mu.RLock()
	c, ok := r.ctors[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w %q (known: %s)", ErrUnknown, name, strings.Join(r.Names(), ", "))
	}
	defer func() {
		if rec := recover(); rec != nil {
			p, err = nil, fmt.Errorf("factory: constructor %q panicked: %v", name, rec)
		}
	}()
	cp := make(Config, len(cfg))
	for k, v := range cfg {
		cp[k] = v
	}
	p, err = c(cp)
	if err != nil {
		return nil, fmt.Errorf("factory: build %q: %w", name, err)
	}
	if p == nil {
		return nil, fmt.Errorf("%w: %q", ErrNilProvider, name)
	}
	return p, nil
}

// Spec says which provider a tenant uses and how it is configured.
type Spec struct {
	Provider string
	Config   Config
}

// Build constructs one provider per tenant. Misconfiguration must fail
// startup loudly and completely: if any tenant fails, Build returns a nil map
// and an error joining ALL failures (each naming its tenant), not just the
// first, so one deploy fixes everything.
func (r *Registry) Build(specs map[string]Spec) (map[string]Provider, error) {
	tenants := make([]string, 0, len(specs))
	for t := range specs {
		tenants = append(tenants, t)
	}
	sort.Strings(tenants)

	out := make(map[string]Provider, len(specs))
	var errs []error
	for _, t := range tenants {
		p, err := r.New(specs[t].Provider, specs[t].Config)
		if err != nil {
			errs = append(errs, fmt.Errorf("tenant %q: %w", t, err))
			continue
		}
		out[t] = p
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

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
)

var errNotImplemented = errors.New("factory: not implemented")

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
	// TODO(lab): a lock and a map from name to Constructor.
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	// TODO(lab)
	return &Registry{}
}

// Register adds a constructor. An empty name or nil constructor is ErrInvalid;
// a name that is already taken is ErrDuplicate (wrapped with the name). Unlike
// a fee schedule, a provider name is an identity: silently replacing it would
// change which vendor a tenant talks to. The first registration stays in effect.
func (r *Registry) Register(name string, c Constructor) error {
	// TODO(lab)
	_, _ = name, c
	return errNotImplemented
}

// Names returns the registered provider names, sorted.
func (r *Registry) Names() []string {
	// TODO(lab)
	return nil
}

// New builds the provider registered as name.
//
// Contract: an unknown name is ErrUnknown, and the message names the
// requested provider and lists the known ones. The constructor is called with
// a COPY of cfg and WITHOUT the registry lock held, so it may itself call
// New (a failover provider built from two others). A constructor error is
// wrapped with the name (errors.Is still works); a constructor panic is
// recovered into an error; a (nil, nil) result is ErrNilProvider.
func (r *Registry) New(name string, cfg Config) (Provider, error) {
	// TODO(lab)
	_, _ = name, cfg
	return nil, errNotImplemented
}

// Spec says which provider a tenant uses and how it is configured.
type Spec struct {
	Provider string
	Config   Config
}

// Build constructs one provider per tenant. Misconfiguration must fail
// startup loudly and completely: if any tenant fails, Build returns a nil map
// and an error joining ALL failures (each naming its tenant), not just the
// first, so one deploy fixes everything. Process tenants in sorted order so
// the message is deterministic.
func (r *Registry) Build(specs map[string]Spec) (map[string]Provider, error) {
	// TODO(lab)
	_ = specs
	return nil, errNotImplemented
}

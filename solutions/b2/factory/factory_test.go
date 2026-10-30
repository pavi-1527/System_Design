package factory

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

// Everything below is defined in this test file only. The registry package
// knows none of these types: that is the extension property under test.

type acme struct{ endpoint string }

func (a *acme) Name() string { return "acme" }
func (a *acme) Fetch(_ context.Context, id string) ([]byte, error) {
	return []byte(a.endpoint + "/" + id), nil
}

func newAcme(cfg Config) (Provider, error) {
	if cfg["endpoint"] == "" {
		return nil, errors.New("acme: endpoint is required")
	}
	return &acme{endpoint: cfg["endpoint"]}, nil
}

type pixelcorp struct{}

func (pixelcorp) Name() string { return "pixelcorp" }
func (pixelcorp) Fetch(context.Context, string) ([]byte, error) {
	return []byte("px"), nil
}

type failover struct{ primary, secondary Provider }

func (f failover) Name() string {
	return "failover(" + f.primary.Name() + "," + f.secondary.Name() + ")"
}
func (f failover) Fetch(ctx context.Context, id string) ([]byte, error) {
	if b, err := f.primary.Fetch(ctx, id); err == nil {
		return b, nil
	}
	return f.secondary.Fetch(ctx, id)
}

func mustRegister(t *testing.T, r *Registry, name string, c Constructor) {
	t.Helper()
	if err := r.Register(name, c); err != nil {
		t.Fatalf("Register(%q): %v", name, err)
	}
}

func TestRegistry_NewVendorNeedsOnlyNewCode(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "acme", newAcme)
	mustRegister(t, r, "pixelcorp", func(Config) (Provider, error) { return pixelcorp{}, nil })

	p, err := r.New("acme", Config{"endpoint": "https://img.acme.example"})
	if err != nil {
		t.Fatalf("New(acme): %v", err)
	}
	b, err := p.Fetch(context.Background(), "chk-9")
	if err != nil || string(b) != "https://img.acme.example/chk-9" {
		t.Errorf("Fetch = %q, %v", b, err)
	}
	if q, err := r.New("pixelcorp", nil); err != nil || q.Name() != "pixelcorp" {
		t.Errorf("New(pixelcorp) = %v, %v", q, err)
	}
	if got, want := r.Names(), []string{"acme", "pixelcorp"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Names() = %v, want sorted %v", got, want)
	}
}

func TestRegister_RejectsBadAndDuplicateRegistrations(t *testing.T) {
	r := NewRegistry()
	if err := r.Register("", newAcme); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty name: %v, want ErrInvalid", err)
	}
	if err := r.Register("x", nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("nil constructor: %v, want ErrInvalid", err)
	}
	mustRegister(t, r, "acme", newAcme)
	err := r.Register("acme", func(Config) (Provider, error) { return pixelcorp{}, nil })
	if !errors.Is(err, ErrDuplicate) || !strings.Contains(err.Error(), "acme") {
		t.Errorf("duplicate: %v, want ErrDuplicate naming acme", err)
	}
	// The first registration must still be the one in effect.
	if p, err := r.New("acme", Config{"endpoint": "e"}); err != nil || p.Name() != "acme" {
		t.Errorf("after a rejected duplicate, New(acme) = %v, %v; the original must win", p, err)
	}
}

func TestNew_UnknownProviderNamesItAndListsTheKnownOnes(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "acme", newAcme)
	mustRegister(t, r, "pixelcorp", func(Config) (Provider, error) { return pixelcorp{}, nil })
	_, err := r.New("acem", nil) // a typo in a tenant's config
	if !errors.Is(err, ErrUnknown) {
		t.Fatalf("err = %v, want ErrUnknown", err)
	}
	for _, want := range []string{"acem", "acme", "pixelcorp"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q so the typo is obvious", err, want)
		}
	}
}

func TestNew_ConstructorErrorsKeepTheirCause(t *testing.T) {
	r := NewRegistry()
	cause := errors.New("bad credentials reference")
	mustRegister(t, r, "acme", func(Config) (Provider, error) { return nil, cause })
	p, err := r.New("acme", nil)
	if p != nil || !errors.Is(err, cause) || !strings.Contains(err.Error(), "acme") {
		t.Errorf("New = %v, %v; want nil provider and an error wrapping the cause and naming acme", p, err)
	}
}

func TestNew_ContainsConstructorPanicsAndNilProviders(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "boom", func(Config) (Provider, error) { panic("nil map in vendor SDK") })
	mustRegister(t, r, "empty", func(Config) (Provider, error) { return nil, nil })

	p, err := r.New("boom", nil)
	if p != nil || err == nil || !strings.Contains(err.Error(), "nil map in vendor SDK") {
		t.Errorf("panicking constructor: %v, %v; want nil and an error carrying the panic value", p, err)
	}
	if p, err := r.New("empty", nil); p != nil || !errors.Is(err, ErrNilProvider) {
		t.Errorf("(nil, nil) constructor: %v, %v; want ErrNilProvider", p, err)
	}
}

func TestNew_ConstructorGetsACopyOfTheConfig(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "scrubber", func(cfg Config) (Provider, error) {
		delete(cfg, "api_key") // constructors scrub secrets from the map they were given
		return pixelcorp{}, nil
	})
	shared := Config{"api_key": "s3cret", "endpoint": "e"}
	for i := 0; i < 2; i++ {
		if _, err := r.New("scrubber", shared); err != nil {
			t.Fatal(err)
		}
	}
	if shared["api_key"] != "s3cret" {
		t.Error("the constructor mutated the caller's Config; New must pass a copy (two tenants may share one map)")
	}
}

func TestBuild_ReportsEveryMisconfiguredTenant(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "acme", newAcme)
	mustRegister(t, r, "pixelcorp", func(Config) (Provider, error) { return pixelcorp{}, nil })

	ok, err := r.Build(map[string]Spec{
		"cu-alpha": {Provider: "acme", Config: Config{"endpoint": "e1"}},
		"cu-beta":  {Provider: "pixelcorp"},
	})
	if err != nil || len(ok) != 2 || ok["cu-alpha"].Name() != "acme" || ok["cu-beta"].Name() != "pixelcorp" {
		t.Fatalf("valid Build = %v, %v", ok, err)
	}

	got, err := r.Build(map[string]Spec{
		"cu-alpha": {Provider: "acme", Config: Config{"endpoint": "e1"}},
		"cu-beta":  {Provider: "acem"},                   // typo
		"cu-gamma": {Provider: "acme", Config: Config{}}, // missing endpoint
	})
	if got != nil || err == nil {
		t.Fatalf("Build with two bad tenants = %v, %v; want nil and an error", got, err)
	}
	for _, want := range []string{"cu-beta", "cu-gamma"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Build error %q does not mention %s: report ALL failures at once", err, want)
		}
	}
	if strings.Contains(err.Error(), "cu-alpha") {
		t.Errorf("Build error %q blames the healthy tenant cu-alpha", err)
	}
	if !errors.Is(err, ErrUnknown) {
		t.Error("joined error should still satisfy errors.Is(ErrUnknown)")
	}
}

func TestNew_ConstructorRunsWithoutTheRegistryLockAndMayReenter(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "acme", newAcme)
	mustRegister(t, r, "pixelcorp", func(Config) (Provider, error) { return pixelcorp{}, nil })
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	releaseAll := func() { once.Do(func() { close(release) }) }
	defer releaseAll()
	mustRegister(t, r, "failover", func(cfg Config) (Provider, error) {
		close(entered)
		<-release
		a, err := r.New(cfg["primary"], Config{"endpoint": "e"}) // re-enters the registry
		if err != nil {
			return nil, err
		}
		b, err := r.New(cfg["secondary"], nil)
		if err != nil {
			return nil, err
		}
		return failover{a, b}, nil
	})

	type result struct {
		p   Provider
		err error
	}
	res := make(chan result, 1)
	go func() {
		p, err := r.New("failover", Config{"primary": "acme", "secondary": "pixelcorp"})
		res <- result{p, err}
	}()
	select {
	case <-entered:
	case r := <-res:
		t.Fatalf("New returned (%v, %v) without running the constructor", r.p, r.err)
	case <-time.After(testTimeout):
		t.Fatal("constructor never started")
	}

	registered := make(chan struct{})
	go func() {
		_ = r.Register("late", newAcme) // a writer arrives while a constructor is running
		close(registered)
	}()
	select {
	case <-registered:
	case <-time.After(testTimeout):
		t.Fatal("Register blocked while a constructor was running: New must not hold the registry lock while it constructs")
	}

	releaseAll()
	select {
	case got := <-res:
		if got.err != nil || got.p.Name() != "failover(acme,pixelcorp)" {
			t.Errorf("failover = %v, %v", got.p, got.err)
		}
	case <-time.After(testTimeout):
		t.Fatal("a constructor that calls New deadlocked")
	}
}

func TestRegistry_ConcurrentUse(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, "acme", newAcme)
	var built atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				if _, err := r.New("acme", Config{"endpoint": "e"}); err != nil {
					t.Errorf("New: %v", err)
					return
				}
				built.Add(1)
				_ = r.Names()
			}
		}()
	}
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				_ = r.Register(fmt.Sprintf("p-%d-%d", g, i), newAcme)
			}
		}()
	}
	wg.Wait()
	if built.Load() != 8*300 {
		t.Errorf("built %d providers, want %d", built.Load(), 8*300)
	}
	if n := len(r.Names()); n != 1+4*100 {
		t.Errorf("Names() has %d entries, want %d: concurrent registrations were lost", n, 1+4*100)
	}
}

func TestRegister_ConcurrentSameNameExactlyOneWins(t *testing.T) {
	r := NewRegistry()
	const n = 32
	var winners, dups atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			switch err := r.Register("acme", newAcme); {
			case err == nil:
				winners.Add(1)
			case errors.Is(err, ErrDuplicate):
				dups.Add(1)
			default:
				t.Errorf("unexpected error %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if winners.Load() != 1 || dups.Load() != n-1 {
		t.Errorf("%d winners, %d duplicates; want 1 and %d (check-then-act without a lock?)", winners.Load(), dups.Load(), n-1)
	}
}

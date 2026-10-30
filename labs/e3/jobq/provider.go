package jobq

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// This file is provided: a fake notification vendor ("send an SMS / push to a
// member") that behaves like the real ones in the ways that matter here. It is
// the system under test's dependency, not part of the exercise. Read it; don't
// edit it.
//
//	POST /send     Idempotency-Key required. Body: a JSON object; its "class"
//	               field selects reject rules. 200 {"effect":"created"} the
//	               first time a key succeeds, 200 {"effect":"duplicate"} after
//	               that (no second effect: this is how a vendor that honours
//	               idempotency keys behaves). Fault injection may answer 503,
//	               429 (+ Retry-After) or 400 instead, or answer slowly.
//	POST /control  Replace the fault settings (ProviderControl). 204.
//	GET  /stats    ProviderStats.
//
// A slow request is slow *before* the effect is recorded, and the effect is
// recorded even if the caller has given up: a vendor that accepted your
// request does not undo it because your timeout fired.

// ProviderControl is the body of POST /control. Every POST replaces all of it.
type ProviderControl struct {
	FailNext     int    `json:"fail_next"`     // answer the next N sends with FailStatus
	FailStatus   int    `json:"fail_status"`   // default 503
	RetryAfter   int    `json:"retry_after"`   // seconds; sent with failures when > 0
	SlowNext     int    `json:"slow_next"`     // the next N sends take SlowMS first
	SlowMS       int    `json:"slow_ms"`       //
	LatencyMS    int    `json:"latency_ms"`    // every send takes at least this long
	RejectClass  string `json:"reject_class"`  // payloads with this "class" always fail...
	RejectStatus int    `json:"reject_status"` // ...with this status (default 400)
}

// ProviderStats is GET /stats.
type ProviderStats struct {
	Requests      int64              `json:"requests"`
	Effects       int64              `json:"effects"`    // distinct keys delivered
	Duplicates    int64              `json:"duplicates"` // successful sends for a key already delivered
	Failed        int64              `json:"failed"`     // answered by FailNext
	Rejected      int64              `json:"rejected"`   // answered by RejectClass or a missing key
	InFlight      int64              `json:"in_flight"`
	MaxInFlight   int64              `json:"max_in_flight"`
	EffectsByKey  map[string]int     `json:"effects_by_key"`
	AttemptsByKey map[string][]int64 `json:"attempts_by_key"` // arrival times, Unix milliseconds
}

type provider struct {
	mu       sync.Mutex
	ctl      ProviderControl
	st       ProviderStats
	inFlight int64
}

func runProvider(ctx context.Context, addr string, stdout, stderr io.Writer) error {
	p := &provider{st: ProviderStats{EffectsByKey: map[string]int{}, AttemptsByKey: map[string][]int64{}}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /send", p.send)
	mux.HandleFunc("POST /control", p.control)
	mux.HandleFunc("GET /stats", p.stats)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return serveOn(ctx, ln, mux, stdout, 5*time.Second)
}

func (p *provider) send(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	var body struct {
		Class string `json:"class"`
	}
	bodyErr := readJSON(r, &body)

	p.mu.Lock()
	p.st.Requests++
	p.inFlight++
	p.st.InFlight = p.inFlight
	p.st.MaxInFlight = max(p.st.MaxInFlight, p.inFlight)
	if key != "" {
		p.st.AttemptsByKey[key] = append(p.st.AttemptsByKey[key], time.Now().UnixMilli())
	}
	delay := time.Duration(p.ctl.LatencyMS) * time.Millisecond
	if p.ctl.SlowNext > 0 {
		p.ctl.SlowNext--
		delay = max(delay, time.Duration(p.ctl.SlowMS)*time.Millisecond)
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.inFlight--
		p.st.InFlight = p.inFlight
		p.mu.Unlock()
	}()

	time.Sleep(delay) // deliberately not r.Context(): a vendor finishes what it accepted

	// Decide under the lock, write the response after releasing it.
	status, msg, retryAfter := http.StatusOK, "", 0
	p.mu.Lock()
	switch {
	case key == "" || bodyErr != nil:
		p.st.Rejected++
		status, msg = http.StatusBadRequest, "Idempotency-Key header and a JSON object body are required"
	case p.ctl.FailNext > 0:
		p.ctl.FailNext--
		p.st.Failed++
		status, msg, retryAfter = p.ctl.FailStatus, "injected failure", p.ctl.RetryAfter
		if status == 0 {
			status = http.StatusServiceUnavailable
		}
	case p.ctl.RejectClass != "" && body.Class == p.ctl.RejectClass:
		p.st.Rejected++
		status, msg = p.ctl.RejectStatus, "rejected payload class "+body.Class
		if status == 0 {
			status = http.StatusBadRequest
		}
	case p.st.EffectsByKey[key] > 0:
		p.st.Duplicates++
		msg = "duplicate"
	default:
		p.st.EffectsByKey[key]++
		p.st.Effects++
		msg = "created"
	}
	p.mu.Unlock()

	if status != http.StatusOK {
		if retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		}
		httpError(w, status, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"effect": msg})
}

func (p *provider) control(w http.ResponseWriter, r *http.Request) {
	var c ProviderControl
	if err := readJSON(r, &c); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	p.mu.Lock()
	p.ctl = c
	p.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (p *provider) stats(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	b, _ := json.Marshal(p.st) // marshal under the lock: the maps are live
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

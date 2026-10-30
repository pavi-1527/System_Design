// Package estimate generates randomized back-of-envelope problems (QPS, storage,
// bandwidth, servers, cache memory, concurrency, availability), checks answers within a
// relative tolerance, and produces worked solutions. Every input the learner needs is
// stated in the prompt; the skill being drilled is the arithmetic and the units.
package estimate

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"

	"github.com/chethu007/system-design-course/internal/quantity"
)

// Problem is one generated estimation exercise. Answer is in base units of Unit's dimension.
type Problem struct {
	Kind     string   `json:"kind"`
	Prompt   string   `json:"prompt"`
	Unit     string   `json:"unit"`
	Answer   float64  `json:"answer"`
	Steps    []string `json:"steps"`
	Decision string   `json:"decision"`
}

// Result is the outcome of checking an answer.
type Result struct {
	Within   bool    `json:"within"`
	Given    float64 `json:"given"`
	Expected float64 `json:"expected"`
	Ratio    float64 `json:"ratio"`
	Note     string  `json:"note,omitempty"`
	Display  string  `json:"display"`
}

// Kinds lists every problem kind.
func Kinds() []string {
	return []string{"qps", "storage", "bandwidth", "servers", "cache", "concurrency", "availability"}
}

var generators = map[string]func(*rand.Rand) Problem{
	"qps":          genQPS,
	"storage":      genStorage,
	"bandwidth":    genBandwidth,
	"servers":      genServers,
	"cache":        genCache,
	"concurrency":  genConcurrency,
	"availability": genAvailability,
}

// Generate makes a problem of the given kind ("" or "any" picks one at random).
func Generate(kind string, rng *rand.Rand) (Problem, error) {
	if kind == "" || kind == "any" {
		ks := Kinds()
		kind = ks[rng.IntN(len(ks))]
	}
	g, ok := generators[kind]
	if !ok {
		return Problem{}, fmt.Errorf("unknown kind %q (one of %s)", kind, strings.Join(Kinds(), ", "))
	}
	p := g(rng)
	p.Kind = kind
	return p, nil
}

// Check parses input in the problem's unit and compares it within tol (relative).
func Check(p Problem, input string, tol float64) (Result, error) {
	u := quantity.ParseUnit(p.Unit)
	got, note, err := u.Parse(input)
	if err != nil {
		return Result{}, err
	}
	r := Result{Given: got, Expected: p.Answer, Note: note, Within: quantity.Within(got, p.Answer, tol)}
	if p.Answer != 0 {
		r.Ratio = got / p.Answer
	}
	r.Display = Show(p.Answer, p.Unit)
	return r, nil
}

// Diagnose turns the ratio of a wrong answer to the reference into a hint about the
// likely mistake.
func Diagnose(ratio float64) string {
	near := func(x float64) bool { return quantity.RelErr(ratio, x) < 0.2 }
	switch {
	case near(8) || near(1.0/8):
		return "Off by ~8×: bits vs bytes."
	case near(1000) || near(1.0/1000) || near(1e6) || near(1e-6):
		return "Off by a power of 1000: check your k/M/G/T prefixes."
	case near(10) || near(0.1) || near(100) || near(0.01):
		return "Off by a power of ten: recount zeros, or write each factor in scientific notation."
	case near(3) || near(1.0/3):
		return "Off by ~3×: did you apply (or forget) replication or the peak factor?"
	case near(2) || near(0.5) || near(5) || near(0.2):
		return "Off by 2–5×: check the peak factor, read:write multiplier, or utilisation."
	case near(365) || near(1.0/365) || near(30) || near(1.0/30) || near(24) || near(1.0/24) || near(60) || near(1.0/60):
		return "Off by a time-unit factor: per day vs per year/month, hours vs minutes."
	case near(86400) || near(1.0/86400):
		return "Off by 86,400: forgot to convert per-day to per-second (or the reverse)."
	}
	return "Re-derive step by step and carry units on every line."
}

// Show renders a base-unit value in the problem's unit for humans.
func Show(base float64, unit string) string {
	u := quantity.ParseUnit(unit)
	switch u.Dim {
	case quantity.Count:
		return quantity.Format(base, u.Dim) + " " + unit
	case quantity.Duration:
		return quantity.Trim(base/u.Scale) + " " + unit
	default:
		return quantity.Format(base, u.Dim)
	}
}

func pick[T any](rng *rand.Rand, xs ...T) T { return xs[rng.IntN(len(xs))] }

func n(v float64) string { return quantity.Format(v, quantity.Count) }

func b(v float64) string { return quantity.Format(v, quantity.Bytes) }

type app struct {
	name, action string
	perUser      []float64
	readsPer     []float64
	size         []float64 // bytes per item
	sizeWhat     string
}

var apps = []app{
	{"a photo-sharing app", "photo uploads", []float64{0.5, 1, 2}, []float64{20, 50, 100}, []float64{200e3, 500e3, 2e6}, "compressed photo"},
	{"a messaging app", "messages sent", []float64{20, 40, 80}, []float64{1, 2, 5}, []float64{200, 500, 1e3}, "message incl. metadata"},
	{"a short-video app", "video uploads", []float64{0.05, 0.1, 0.2}, []float64{100, 300, 1000}, []float64{20e6, 50e6}, "transcoded video (all renditions)"},
	{"a payments app", "payments", []float64{1, 2, 4}, []float64{3, 5, 10}, []float64{1e3, 2e3}, "ledger entries + audit record"},
	{"a news-feed app", "posts", []float64{0.5, 1, 2}, []float64{50, 100, 200}, []float64{1e3, 2e3}, "post incl. metadata"},
	{"a URL shortener", "new short links", []float64{0.1, 0.2, 0.5}, []float64{10, 50, 100}, []float64{500, 1e3}, "mapping row incl. index"},
	{"an IoT telemetry platform", "sensor readings", []float64{1440, 2880, 8640}, []float64{0.1, 0.5}, []float64{100, 200}, "reading"},
}

var daus = []float64{1e6, 2e6, 5e6, 10e6, 20e6, 50e6, 100e6, 200e6, 500e6}

func genQPS(rng *rand.Rand) Problem {
	a := pick(rng, apps...)
	dau := pick(rng, daus...)
	per := pick(rng, a.perUser...)
	pf := pick(rng, 2.0, 3.0, 5.0)
	avg := dau * per / 86400
	steps := []string{
		fmt.Sprintf("Writes per day = %s DAU × %g %s/user = %s/day", n(dau), per, a.action, n(dau*per)),
		fmt.Sprintf("Average write QPS = %s / 86,400 s ≈ %s/s  (napkin: ÷10^5 ≈ %s/s, then remember it's ~14%% low)", n(dau*per), n(avg), n(dau*per/1e5)),
	}
	switch rng.IntN(3) {
	case 0:
		return Problem{
			Prompt:   fmt.Sprintf("%s has %s daily active users; each makes %g %s per day on average. What is the AVERAGE write QPS?", capital(a.name), n(dau), per, a.action),
			Unit:     "req/s",
			Answer:   avg,
			Steps:    steps,
			Decision: "Average QPS sizes steady-state cost; capacity is planned on the peak, so always ask for the peak factor next.",
		}
	case 1:
		steps = append(steps, fmt.Sprintf("Peak = average × %g ≈ %s/s", pf, n(avg*pf)))
		return Problem{
			Prompt:   fmt.Sprintf("%s has %s DAU; each makes %g %s per day. Peak traffic is %g× the daily average. What is the PEAK write QPS?", capital(a.name), n(dau), per, a.action, pf),
			Unit:     "req/s",
			Answer:   avg * pf,
			Steps:    steps,
			Decision: decideWrites(avg * pf),
		}
	default:
		reads := pick(rng, a.readsPer...)
		steps = append(steps,
			fmt.Sprintf("Read QPS (avg) = write QPS × %g reads per item ≈ %s/s", reads, n(avg*reads)),
			fmt.Sprintf("Peak read QPS = %s × %g ≈ %s/s", n(avg*reads), pf, n(avg*reads*pf)))
		return Problem{
			Prompt:   fmt.Sprintf("%s has %s DAU; each makes %g %s per day, and each item is read %g times on average. Peak is %g× average. What is the PEAK READ QPS?", capital(a.name), n(dau), per, a.action, reads, pf),
			Unit:     "req/s",
			Answer:   avg * reads * pf,
			Steps:    steps,
			Decision: decideReads(avg * reads * pf),
		}
	}
}

func decideWrites(q float64) string {
	switch {
	case q < 1000:
		return "Under ~1k writes/s a single well-indexed relational primary is comfortable (≈ 1k–10k TPS per primary, see docs/NUMBERS.md)."
	case q < 10000:
		return "1k–10k writes/s is near the limit of one relational primary: batch writes, keep transactions short, and plan the partitioning key now."
	default:
		return "Beyond ~10k writes/s you need write partitioning (sharding) or a log-structured store; one primary will not keep up."
	}
}

func decideReads(q float64) string {
	switch {
	case q < 10000:
		return "Under ~10k reads/s, read replicas or a modest cache are enough."
	case q < 200000:
		return "Tens of thousands of reads/s: put a cache in front (Redis ≈ 100k+ ops/s per node) and expect a hit rate target to drive DB sizing."
	default:
		return "Hundreds of thousands of reads/s: a sharded cache tier and/or CDN is mandatory; the DB should only see misses."
	}
}

func genStorage(rng *rand.Rand) Problem {
	a := pick(rng, apps...)
	dau := pick(rng, daus...)
	per := pick(rng, a.perUser...)
	size := pick(rng, a.size...)
	years := pick(rng, 1.0, 3.0, 5.0, 10.0)
	rep := pick(rng, 1.0, 3.0)
	perDay := dau * per
	total := perDay * size * 365 * years * rep
	repText := "Ignore replication (it's a single copy, e.g. object storage quoted per logical byte)."
	if rep > 1 {
		repText = fmt.Sprintf("Data is stored with replication factor %g.", rep)
	}
	return Problem{
		Prompt: fmt.Sprintf("%s has %s DAU, each making %g %s per day. Each %s is %s. Data is kept for %g years. %s How much total storage is needed?",
			capital(a.name), n(dau), per, a.action, a.sizeWhat, b(size), years, repText),
		Unit:   "bytes",
		Answer: total,
		Steps: []string{
			fmt.Sprintf("Items per day = %s × %g = %s", n(dau), per, n(perDay)),
			fmt.Sprintf("Bytes per day = %s × %s = %s", n(perDay), b(size), b(perDay*size)),
			fmt.Sprintf("Per year = × 365 = %s", b(perDay*size*365)),
			fmt.Sprintf("× %g years × replication %g = %s", years, rep, b(total)),
		},
		Decision: decideStorage(total),
	}
}

func decideStorage(t float64) string {
	switch {
	case t < 1e12:
		return "Under ~1 TB fits on one machine's disk; replication is for availability, not capacity."
	case t < 10e12:
		return "A few TB: one large DB node can hold it, but backups, restores and index rebuilds get slow; plan partitioning or archival before it grows 10×."
	case t < 100e12:
		return "Tens of TB: beyond one comfortable DB node; partition, or put blobs in object storage and keep only metadata in the DB."
	default:
		return "Hundreds of TB to PB: object storage with lifecycle tiering (hot → infrequent → archive); cost per GB-month starts to dominate design choices."
	}
}

func genBandwidth(rng *rand.Rand) Problem {
	type item struct {
		what string
		size []float64
	}
	it := pick(rng,
		item{"thumbnail image responses", []float64{20e3, 50e3}},
		item{"full-size photo responses", []float64{500e3, 2e6}},
		item{"API JSON responses", []float64{2e3, 10e3}},
		item{"video segments (4-second chunks)", []float64{1e6, 2.5e6}},
	)
	q := pick(rng, 500.0, 2000, 5000, 10000, 50000)
	size := pick(rng, it.size...)
	bps := q * size * 8
	return Problem{
		Prompt: fmt.Sprintf("At peak your service serves %s req/s of %s averaging %s each. What egress bandwidth is that, in Gbps?",
			n(q), it.what, b(size)),
		Unit:   "Gbps",
		Answer: bps,
		Steps: []string{
			fmt.Sprintf("Bytes/s = %s req/s × %s = %s/s", n(q), b(size), b(q*size)),
			fmt.Sprintf("Bits/s = × 8 = %s  (network is quoted in bits!)", quantity.Format(bps, quantity.Bits)),
		},
		Decision: decideBandwidth(bps),
	}
}

func decideBandwidth(bps float64) string {
	switch {
	case bps < 5e9:
		return "A few Gbps: a handful of servers with 10–25 Gbps NICs can serve it, but egress cost (≈ $0.05–0.09/GB) may already justify a CDN."
	case bps < 100e9:
		return "Tens of Gbps: serve from a CDN; origin only handles misses. Compute egress cost per month before choosing."
	default:
		return "Hundreds of Gbps+: CDN is mandatory and multi-CDN/edge caching strategy becomes a core design topic."
	}
}

func genServers(rng *rand.Rand) Problem {
	peak := pick(rng, 3000.0, 8000, 15000, 40000, 120000)
	per := pick(rng, 500.0, 1000, 2000, 4000)
	util := pick(rng, 0.5, 0.6, 0.7)
	spares := pick(rng, 1, 2)
	base := math.Ceil(peak / (per * util))
	total := base + float64(spares)
	return Problem{
		Prompt: fmt.Sprintf("Peak load is %s req/s. Load tests show one app server saturates at %s req/s. You run servers at most %.0f%% utilised and keep N+%d spare capacity for failures and deploys. How many servers?",
			n(peak), n(per), util*100, spares),
		Unit:   "servers",
		Answer: total,
		Steps: []string{
			fmt.Sprintf("Usable per server = %s × %.0f%% = %s req/s", n(per), util*100, n(per*util)),
			fmt.Sprintf("Needed = ceil(%s / %s) = %.0f", n(peak), n(per*util), base),
			fmt.Sprintf("+ %d spares = %.0f servers", spares, total),
		},
		Decision: "Server count drives cost and blast radius: if it's in the hundreds, invest in autoscaling and per-instance efficiency; if it's 3–5, availability (N+2, multi-AZ) dominates the count, not load.",
	}
}

func genCache(rng *rand.Rand) Problem {
	items := pick(rng, 10e6, 50e6, 100e6, 500e6)
	size := pick(rng, 500.0, 1e3, 2e3, 10e3)
	frac := pick(rng, 0.1, 0.2, 0.3)
	over := pick(rng, 50.0, 100)
	total := items * frac * (size + over)
	return Problem{
		Prompt: fmt.Sprintf("Your service reads %s distinct objects per day, averaging %s each. You will cache the hottest %.0f%% of them. Assume %s of per-entry overhead (key, pointers, allocator). How much cache memory is needed?",
			n(items), b(size), frac*100, b(over)),
		Unit:   "bytes",
		Answer: total,
		Steps: []string{
			fmt.Sprintf("Entries = %s × %.0f%% = %s", n(items), frac*100, n(items*frac)),
			fmt.Sprintf("Bytes per entry = %s + %s = %s", b(size), b(over), b(size+over)),
			fmt.Sprintf("Total = %s × %s = %s", n(items*frac), b(size+over), b(total)),
		},
		Decision: decideCache(total),
	}
}

func decideCache(t float64) string {
	switch {
	case t < 32e9:
		return "Fits in one cache node's RAM (with replicas for availability); sharding is not needed for capacity."
	case t < 500e9:
		return "Tens to hundreds of GB: a sharded cache cluster (consistent hashing, see A4) with a few nodes."
	default:
		return "TB-scale cache: question the hot-set assumption, consider caching smaller projections, or an SSD-backed cache tier."
	}
}

func genConcurrency(rng *rand.Rand) Problem {
	q := pick(rng, 500.0, 2000, 5000, 20000)
	ms := pick(rng, 5.0, 20, 50, 200, 800)
	what := pick(rng,
		"each request holds a database connection for",
		"each request waits on a vendor API call for",
		"each request is in the system (end to end) for",
	)
	l := q * ms / 1000
	return Problem{
		Prompt: fmt.Sprintf("Peak load is %s req/s and %s %g ms on average. How many are in flight concurrently (connections/requests)?", n(q), what, ms),
		Unit:   "connections",
		Answer: l,
		Steps: []string{
			"Little's law: L = λ × W (concurrency = arrival rate × time in system)",
			fmt.Sprintf("L = %s/s × %g s = %s", n(q), ms/1000, n(l)),
		},
		Decision: "This sizes pools: a DB pool or vendor client pool smaller than L queues requests (latency spikes); far larger can overload the downstream. Remember latency rises under load, so L rises with it.",
	}
}

func genAvailability(rng *rand.Rand) Problem {
	slos := []float64{0.995, 0.999, 0.9995, 0.9999}
	names := []string{"API gateway", "auth service", "core service", "database", "vendor adapter"}
	rng.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
	if rng.IntN(2) == 0 {
		k := 2 + rng.IntN(3)
		prod := 1.0
		var parts, steps []string
		for i := 0; i < k; i++ {
			s := pick(rng, slos...)
			prod *= s
			parts = append(parts, fmt.Sprintf("%s %s%%", names[i], pct(s)))
		}
		down := (1 - prod) * 30 * 24 * 3600
		steps = append(steps,
			"Serial dependencies multiply: A = "+strings.Join(parts, " × "),
			fmt.Sprintf("A ≈ %.5f → unavailability %.4f%%", prod, (1-prod)*100),
			fmt.Sprintf("Downtime per 30 days = (1 − A) × 43,200 min ≈ %s min", quantity.Trim(down/60)))
		return Problem{
			Prompt:   fmt.Sprintf("A request must pass through, in series: %s. Assume independent failures. What is the expected downtime per 30-day month, in minutes?", strings.Join(parts, ", ")),
			Unit:     "minutes",
			Answer:   down,
			Steps:    steps,
			Decision: "Your SLO can't exceed the product of your hard dependencies; the weakest link dominates. Remove a dependency from the critical path (cache, async, fallback) before buying another nine elsewhere.",
		}
	}
	r := pick(rng, 0.99, 0.995)
	db := pick(rng, 0.999, 0.9995)
	par := 1 - (1-r)*(1-r)
	total := par * db
	down := (1 - total) * 365 * 24 * 3600
	return Problem{
		Prompt: fmt.Sprintf("Two independent app replicas, each %s%% available, sit behind a load balancer (either one can serve). Every request also needs the database (%s%%). Ignoring the load balancer itself, what is the expected downtime per year, in minutes?",
			pct(r), pct(db)),
		Unit:   "minutes",
		Answer: down,
		Steps: []string{
			fmt.Sprintf("Parallel replicas: 1 − (1 − %g)² = %.6f", r, par),
			fmt.Sprintf("Series with DB: %.6f × %g = %.6f", par, db, total),
			fmt.Sprintf("Downtime/year = (1 − A) × 525,600 min ≈ %s min", quantity.Trim(down/60)),
		},
		Decision: "Redundancy makes the replicas nearly irrelevant; the single database now dominates downtime. The independence assumption is the weak point — shared deploys, config and regions correlate failures.",
	}
}

func pct(s float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", s*100), "0"), ".")
}

func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

//go:build breakit

package burnrate

// Break-it experiments for A8 Unit 2 §3–§4. These are measurements, not
// pass/fail tests: they replay 10 simulated days of traffic through several
// alerting strategies and print what each one would have done.
//
// Run: go test -tags breakit -run BreakIt -v ./labs/a8/burnrate/
//
// To change the scenarios, copy this file to breakit_scratch_test.go (keep the
// //go:build line, rename the test) and edit the copy: the shared test files
// must stay identical to the solution's.

import (
	"fmt"
	"testing"
	"time"
)

// The timeline, in minutes from tBase, 10,000 requests per minute throughout.
const (
	biPerMin        = 10_000
	biBaselineBad   = 5      // 0.05% errors all the time: burn 0.5
	biBlipEvery     = 6 * 60 // a self-healing blip every 6 hours...
	biBlipLen       = 2      // ...lasting 2 minutes...
	biBlipBad       = 500    // ...at 5% errors
	biNoiseEnd      = 3 * 24 * 60
	biOutageStart   = 3*24*60 + 300 // a real incident: 20 minutes of total outage
	biOutageLen     = 20
	biSlowStart     = 7 * 24 * 60 // from day 7: 0.5% errors (burn 5) for two days
	biSlowEnd       = 9 * 24 * 60
	biBrownoutStart = 9*24*60 + 360 // day 9, 06:00: six hours at 10% errors (burn 100)
	biBrownoutLen   = 6 * 60
	biTotal         = 10 * 24 * 60
)

func biBad(m int) uint64 {
	bad := uint64(biBaselineBad)
	switch {
	case m < biNoiseEnd && m%biBlipEvery < biBlipLen && m >= biBlipEvery:
		bad = biBlipBad
	case m >= biOutageStart && m < biOutageStart+biOutageLen:
		bad = biPerMin
	case m >= biSlowStart && m < biSlowEnd:
		bad = 50
	case m >= biBrownoutStart && m < biBrownoutStart+biBrownoutLen:
		bad = 1000
	}
	return bad
}

type biStrategy struct {
	name  string
	rules []Rule
}

type biResult struct {
	noisePages     int // distinct paging episodes during the first 3 days (no real incident there)
	outageDetect   int // minutes from outage start to the first page, -1 if never
	outageReset    int // minutes from outage end until paging stops, -1 if never started
	slowPage       int // hours from slow-burn start to first page, -1 if never
	slowTicket     int // hours from slow-burn start to first ticket, -1 if never
	brownoutDetect int // minutes from brownout start to first page, -1 if never
	episodes       int // distinct alert episodes (page or ticket) over all ten days
	trueEpisodes   int // episodes that began during one of the three real incidents
	detected       int // real incidents (of 3) that raised at least one alert
}

// biIncidents are the three real incidents: [start, end) in minutes.
var biIncidents = [3][2]int{
	{biOutageStart, biOutageStart + biOutageLen + 5},
	{biSlowStart, biSlowEnd},
	{biBrownoutStart, biBrownoutStart + biBrownoutLen},
}

func biRun(t *testing.T, s biStrategy) biResult {
	t.Helper()
	tr, err := NewTracker(Config{Objective: 0.999, Period: 30 * 24 * time.Hour, Resolution: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := NewEvaluator(tr, s.rules)
	if err != nil {
		t.Fatal(err)
	}
	res := biResult{0, -1, -1, -1, -1, -1, 0, 0, 0}
	wasPaging, wasAny := false, false
	var hit [3]bool
	for m := 0; m < biTotal; m++ {
		bad := biBad(m)
		now := tBase.Add(time.Duration(m) * time.Minute)
		if err := tr.RecordAt(now, biPerMin-bad, biPerMin); err != nil {
			t.Fatal(err)
		}
		var paging, ticket bool
		for _, a := range ev.Evaluate(now) {
			if a.Rule.Severity == "page" {
				paging = true
			} else {
				ticket = true
			}
		}
		if paging && !wasPaging && m < biNoiseEnd && m < biOutageStart {
			res.noisePages++
		}
		if paging && m >= biOutageStart && res.outageDetect < 0 && m < biOutageStart+biOutageLen+60 {
			res.outageDetect = m - biOutageStart
		}
		if res.outageDetect >= 0 && res.outageReset < 0 && m >= biOutageStart+biOutageLen && !paging {
			res.outageReset = m - (biOutageStart + biOutageLen)
		}
		if paging && m >= biSlowStart && m < biSlowEnd && res.slowPage < 0 {
			res.slowPage = (m - biSlowStart) / 60
		}
		if ticket && m >= biSlowStart && m < biSlowEnd && res.slowTicket < 0 {
			res.slowTicket = (m - biSlowStart) / 60
		}
		if paging && m >= biBrownoutStart && res.brownoutDetect < 0 {
			res.brownoutDetect = m - biBrownoutStart
		}
		any := paging || ticket
		if any && !wasAny {
			res.episodes++
			for i, in := range biIncidents {
				if m >= in[0] && m < in[1] {
					res.trueEpisodes++
					hit[i] = true
					break
				}
			}
		}
		wasPaging, wasAny = paging, any
	}
	for _, h := range hit {
		if h {
			res.detected++
		}
	}
	return res
}

// TestBreakIt_AlertStrategies replays the timeline through six strategies.
// Predict the table before you run it: which strategy pages on the 2-minute
// blips (false positives), which is slow to notice the outage, which never
// notices the brownout (a 6-hour 10% error rate burns ~83% of a month's
// budget), which keeps paging long after the outage is over?
func TestBreakIt_AlertStrategies(t *testing.T) {
	strategies := []biStrategy{
		{"single window 5m @ 14.4x", []Rule{{Name: "a", Severity: "page", Long: 5 * time.Minute, Threshold: 14.4}}},
		{"single window 1h @ 14.4x", []Rule{{Name: "b", Severity: "page", Long: time.Hour, Threshold: 14.4}}},
		{"1h + 5m @ 14.4x (short window ON)", []Rule{{Name: "c", Severity: "page", Long: time.Hour, Short: 5 * time.Minute, Threshold: 14.4}}},
		{"1h + 5m @ 1.44x  (threshold / 10)", []Rule{{Name: "d", Severity: "page", Long: time.Hour, Short: 5 * time.Minute, Threshold: 1.44}}},
		{"1h + 5m @ 144x   (threshold x 10)", []Rule{{Name: "e", Severity: "page", Long: time.Hour, Short: 5 * time.Minute, Threshold: 144}}},
		{"DefaultRules (3 window pairs)", DefaultRules(30 * 24 * time.Hour)},
	}
	type row struct {
		name string
		r    biResult
	}
	var rows []row
	for _, s := range strategies {
		rows = append(rows, row{s.name, biRun(t, s)})
	}
	f := func(v int, unit string) string {
		if v < 0 {
			return "never"
		}
		return fmt.Sprintf("%d %s", v, unit)
	}
	t.Logf("TIMING          %-34s %14s %13s %13s %13s %16s", "strategy", "outage detect", "outage reset", "slow: page", "slow: ticket", "brownout detect")
	for _, x := range rows {
		t.Logf("                %-34s %14s %13s %13s %13s %16s", x.name,
			f(x.r.outageDetect, "min"), f(x.r.outageReset, "min"), f(x.r.slowPage, "h"), f(x.r.slowTicket, "h"), f(x.r.brownoutDetect, "min"))
	}
	t.Logf("QUALITY         %-34s %11s %9s %10s %10s", "strategy", "noise pages", "episodes", "precision", "recall")
	for _, x := range rows {
		t.Logf("                %-34s %11d %9d %9.0f%% %7d/3", x.name, x.r.noisePages, x.r.episodes,
			100*float64(x.r.trueEpisodes)/float64(max(x.r.episodes, 1)), x.r.detected)
	}
}

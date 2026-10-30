// Command sd is the terminal front end of the course: quizzes, estimation drills,
// spaced review, lab runs, gate tracking and status. All logic lives in internal/...
// packages so other front ends (e.g. a local web UI) can reuse it; this package only
// does terminal I/O.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chethu007/system-design-course/internal/course"
	"github.com/chethu007/system-design-course/internal/progress"
)

const usage = `sd — system design course CLI

Usage:
  sd status [MODULE] [--json]     what's unlocked, in progress, next, needs refresh
  sd progress [--json]            detailed history: attempts, labs, assessments, weak tags
  sd quiz MODULE [-n N]           gated quiz attempt (≥ 85% twice, on different days)
  sd review [-n N]                spaced-repetition review of due questions
  sd recap [--json] [--no-quiz]   summary + warm-up after time away
  sd estimate [-n N] [-kind K]    randomized back-of-envelope drills (kinds: qps, storage,
                                  bandwidth, servers, cache, concurrency, availability)
  sd lab MODULE [--all] [--no-race] [-v]
                                  run the module's lab tests and record the result
  sd record design MODULE --self LEVEL [--tutor LEVEL] [--exercise NAME] [--notes TEXT]
  sd record teachback MODULE --result pass|revise [--notes TEXT]
  sd record mock MODULE --format hld-45|lld-45|mc-90|loop --problem NAME --minutes N --level LEVEL [--notes TEXT]
  sd record note [MODULE] --tags a,b --notes TEXT
                                  record tutor assessments (levels: below, approaching, meets, exceeds)
  sd validate [--json]            check course metadata, quizzes, lesson markup, lab/solution pairs
  sd serve [--port N]             local web UI at http://localhost:8080 (bound to 127.0.0.1)

Environment:
  SD_ROOT      course root (default: nearest parent with course/course.json)
  SD_PROGRESS  progress file (default: $SD_ROOT/progress.json)
`

type app struct {
	c        *course.Course
	p        *progress.Progress
	progPath string
	now      time.Time
	in       *bufio.Scanner
	out      io.Writer
	rng      *rand.Rand
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "sd:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		return nil
	}
	a, err := newApp(stdin, stdout)
	if err != nil {
		return err
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "status":
		return a.cmdStatus(rest)
	case "progress":
		return a.cmdProgress(rest)
	case "quiz":
		return a.cmdQuiz(rest)
	case "review":
		return a.cmdReview(rest)
	case "recap":
		return a.cmdRecap(rest)
	case "estimate":
		return a.cmdEstimate(rest)
	case "lab":
		return a.cmdLab(rest)
	case "record":
		return a.cmdRecord(rest)
	case "validate":
		return a.cmdValidate(rest)
	case "serve":
		return a.cmdServe(rest)
	default:
		return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
	}
}

func newApp(stdin io.Reader, stdout io.Writer) (*app, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root, err := course.FindRoot(wd)
	if err != nil {
		return nil, err
	}
	c, err := course.Load(root)
	if err != nil {
		return nil, err
	}
	pp := os.Getenv("SD_PROGRESS")
	if pp == "" {
		pp = filepath.Join(root, "progress.json")
	}
	p, err := progress.Load(pp)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	// SD_NOW pins the clock (RFC3339). It exists for testing the gate rules
	// (e.g. "two passes on different days"); don't use it to game your own gates.
	if s := os.Getenv("SD_NOW"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil, fmt.Errorf("SD_NOW: %w", err)
		}
		now = t
	}
	sc := bufio.NewScanner(stdin)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	return &app{
		c: c, p: p, progPath: pp, now: now, in: sc, out: stdout,
		rng: rand.New(rand.NewPCG(uint64(now.UnixNano()), 0x5d5d)),
	}, nil
}

func (a *app) save() error { return a.p.Save(a.progPath) }

func (a *app) printf(format string, args ...any) { fmt.Fprintf(a.out, format, args...) }

// ask prints a prompt and reads one line. ok is false on EOF.
func (a *app) ask(prompt string) (string, bool) {
	a.printf("%s", prompt)
	if !a.in.Scan() {
		a.printf("\n")
		return "", false
	}
	return strings.TrimSpace(a.in.Text()), true
}

func (a *app) json(v any) error {
	enc := json.NewEncoder(a.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// splitArgs separates leading positional arguments from flags so that both
// "sd quiz A0 -n 5" and "sd quiz -n 5 A0" work.
func splitArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for len(args) > 0 {
		if !strings.HasPrefix(args[0], "-") {
			pos = append(pos, args[0])
			args = args[1:]
			continue
		}
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
	}
	return pos, nil
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

var errAborted = errors.New("aborted; nothing recorded")

func (a *app) announcePassed(ids []string) {
	for _, id := range ids {
		m, _ := a.c.Module(id)
		a.printf("\n*** GATE PASSED: %s — %s. ***\n", id, m.Title)
		for _, other := range a.c.Ordered() {
			for _, pre := range other.Prerequisites {
				if pre == id {
					a.printf("    %s %s may now be unlocked (check `sd status`).\n", other.ID, other.Title)
				}
			}
		}
	}
}

func pct(f float64) string { return fmt.Sprintf("%.0f%%", f*100) }

func wrap(s string, width int, indent string) string {
	var out strings.Builder
	for i, para := range strings.Split(s, "\n") {
		if i > 0 {
			out.WriteString("\n")
		}
		out.WriteString(indent)
		if len(para) <= width {
			out.WriteString(para) // short lines (including code) keep their spacing
			continue
		}
		line := 0
		for j, w := range strings.Fields(para) {
			if j > 0 && line+1+len(w) > width {
				out.WriteString("\n" + indent)
				line = 0
			} else if j > 0 {
				out.WriteString(" ")
				line++
			}
			out.WriteString(w)
			line += len(w)
		}
	}
	return out.String()
}

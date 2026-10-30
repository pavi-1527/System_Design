// Package labrun runs a module's lab tests with `go test -json` and summarises the
// result per package. Output lines are streamed to a writer so any front end can show them.
package labrun

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/chethu007/system-design-course/internal/course"
	"github.com/chethu007/system-design-course/internal/progress"
)

// Options controls a run.
type Options struct {
	IncludeStretch bool     // also run STRETCH lab packages (they never gate)
	Packages       []string // run only these packages (core or not); empty = all selected by IncludeStretch
	Race           bool     // run with -race (requires cgo)
	Dir            string
	Timeout        time.Duration
	Verbose        bool // stream all output, not just failures and summaries
}

// RaceAvailable reports whether `go test -race` can work here (cgo enabled).
func RaceAvailable(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "go", "env", "CGO_ENABLED").Output()
	return err == nil && strings.TrimSpace(string(out)) == "1"
}

type event struct {
	Action  string
	Package string
	Test    string
	Output  string
}

// Run executes the lab packages of m under root/<m.Paths.Lab> and returns a LabRun.
// Passed is true only when every core package passes.
func Run(ctx context.Context, root string, m *course.Module, opt Options, out io.Writer) (progress.LabRun, error) {
	run := progress.LabRun{At: time.Now(), Race: opt.Race}
	if m.Paths.Lab == "" || len(m.Labs) == 0 {
		return run, fmt.Errorf("module %s has no labs", m.ID)
	}
	modPath, err := modulePath(ctx, root)
	if err != nil {
		return run, err
	}
	only := map[string]bool{}
	for _, p := range opt.Packages {
		only[p] = true
	}
	var pkgs []string
	for _, l := range m.Labs {
		if len(only) > 0 {
			if !only[l.Package] {
				continue
			}
			delete(only, l.Package)
		} else if !l.Core && !opt.IncludeStretch {
			continue
		}
		pkgs = append(pkgs, modPath+"/"+m.Paths.Lab+"/"+l.Package)
		run.Packages = append(run.Packages, progress.LabPackage{Package: l.Package, Core: l.Core})
	}
	for p := range only {
		return run, fmt.Errorf("module %s has no lab package %q", m.ID, p)
	}
	byImport := map[string]*progress.LabPackage{}
	for i, imp := range pkgs {
		byImport[imp] = &run.Packages[i]
	}
	args := []string{"test", "-json", "-count=1"}
	if opt.Race {
		args = append(args, "-race")
	}
	if opt.Timeout > 0 {
		args = append(args, "-timeout", opt.Timeout.String())
	}
	args = append(args, pkgs...)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return run, err
	}
	if err := cmd.Start(); err != nil {
		return run, err
	}
	seenResult := map[string]bool{}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		var ev event
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			fmt.Fprintln(out, sc.Text())
			continue
		}
		lp := byImport[ev.Package]
		switch ev.Action {
		case "output", "build-output":
			if show(ev, opt.Verbose) {
				fmt.Fprint(out, ev.Output)
			}
		case "fail":
			if lp != nil && ev.Test == "" {
				lp.Passed = false
				seenResult[ev.Package] = true
			} else if lp != nil && !strings.Contains(ev.Test, "/") {
				lp.Failed++
			}
		case "pass":
			if lp != nil && ev.Test == "" {
				lp.Passed = true
				seenResult[ev.Package] = true
			}
		}
	}
	waitErr := cmd.Wait()
	if s := strings.TrimSpace(stderr.String()); s != "" {
		fmt.Fprintln(out, s)
	}
	run.Passed = true
	anyCore := false
	for i := range run.Packages {
		lp := &run.Packages[i]
		imp := modPath + "/" + m.Paths.Lab + "/" + lp.Package
		if !seenResult[imp] {
			lp.Passed = false // build failure or crash: no result line
		}
		if lp.Core {
			anyCore = true
			if !lp.Passed {
				run.Passed = false
			}
		}
	}
	if !anyCore {
		run.Passed = false
	}
	if ctx.Err() != nil {
		return run, ctx.Err()
	}
	_ = waitErr // a non-zero exit just means tests failed; per-package results carry that
	return run, nil
}

// show filters go test output: hide RUN/PAUSE/CONT noise and passing lines unless verbose.
func show(ev event, verbose bool) bool {
	o := strings.TrimSpace(ev.Output)
	if verbose {
		return true
	}
	switch {
	case strings.HasPrefix(o, "=== RUN"), strings.HasPrefix(o, "=== PAUSE"), strings.HasPrefix(o, "=== CONT"),
		strings.HasPrefix(o, "=== NAME"), strings.HasPrefix(o, "--- PASS"), o == "PASS":
		return false
	}
	return true
}

func modulePath(ctx context.Context, root string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-m")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go list -m: %w (is Go installed and on PATH?)", err)
	}
	return strings.TrimSpace(string(out)), nil
}

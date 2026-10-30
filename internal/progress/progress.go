// Package progress owns progress.json: the learner's quiz attempts, lab runs, tutor
// assessments, spaced-repetition cards, estimation drill history and activity log.
// It is plain data plus small mutation helpers; gate logic lives in package mastery.
package progress

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SchemaVersion of progress.json.
const SchemaVersion = 1

// MaxActivity bounds the activity log.
const MaxActivity = 500

// Progress is the whole of progress.json.
type Progress struct {
	SchemaVersion int                      `json:"schema_version"`
	Learner       Learner                  `json:"learner"`
	LastActiveAt  *time.Time               `json:"last_active_at,omitempty"`
	Modules       map[string]*ModuleRecord `json:"modules"`
	Review        Review                   `json:"review"`
	Estimate      EstimateHistory          `json:"estimate"`
	Activity      []Activity               `json:"activity"`
	TutorNotes    []TutorNote              `json:"tutor_notes"`
}

// Learner identifies who this file belongs to.
type Learner struct {
	Name      string     `json:"name"`
	StartedAt *time.Time `json:"started_at,omitempty"`
}

// ModuleRecord is everything recorded for one module.
type ModuleRecord struct {
	QuizAttempts []QuizAttempt `json:"quiz_attempts"`
	LabRuns      []LabRun      `json:"lab_runs"`
	Designs      []Assessment  `json:"designs"`
	Teachbacks   []Assessment  `json:"teachbacks"`
	Mocks        []Mock        `json:"mocks"`

	// Written by the web UI (`sd serve`); neither counts towards the gate.
	Predictions map[string]*Prediction `json:"predictions,omitempty"`
	LabAttempts []LabAttempt           `json:"lab_attempts,omitempty"`

	GatePassedAt     *time.Time `json:"gate_passed_at,omitempty"`
	NeedsRefresh     bool       `json:"needs_refresh"`
	RefreshReason    string     `json:"refresh_reason,omitempty"`
	RefreshFlaggedAt *time.Time `json:"refresh_flagged_at,omitempty"`
	RefreshedAt      *time.Time `json:"refreshed_at,omitempty"`
}

// QuizAttempt is one completed `sd quiz` run.
type QuizAttempt struct {
	At      time.Time      `json:"at"`
	Correct int            `json:"correct"`
	Total   int            `json:"total"`
	Score   float64        `json:"score"`
	Answers []AnswerRecord `json:"answers"`
}

// AnswerRecord is the outcome of one question within an attempt or review.
type AnswerRecord struct {
	QID     string `json:"qid"`
	Correct bool   `json:"correct"`
	Given   string `json:"given,omitempty"`
}

// LabRun is one `sd lab` run.
type LabRun struct {
	At       time.Time    `json:"at"`
	Passed   bool         `json:"passed"`
	Race     bool         `json:"race"`
	Packages []LabPackage `json:"packages"`
}

// LabPackage is the result for one lab package in a run.
type LabPackage struct {
	Package string `json:"package"`
	Core    bool   `json:"core"`
	Passed  bool   `json:"passed"`
	Failed  int    `json:"failed_tests"`
}

// LabAttempt is a single-package test run started from the web UI's "Run tests"
// button. It unlocks that package's reference solution but never gates: only a full
// run of every core package (LabRun) does.
type LabAttempt struct {
	At      time.Time `json:"at"`
	Package string    `json:"package"`
	Passed  bool      `json:"passed"`
	Race    bool      `json:"race"`
	Failed  int       `json:"failed_tests"`
}

// Prediction is the learner's written answer to a lesson's predict block, saved when
// they revealed the answer. It is kept as first written so it can be compared later.
type Prediction struct {
	At     time.Time `json:"at"`
	Answer string    `json:"answer"`
}

// Assessment is a design-exercise score or a teach-back verdict, recorded by the tutor.
// For designs, Self and Tutor are rubric levels; for teach-backs, Result is pass|revise.
type Assessment struct {
	At       time.Time `json:"at"`
	Exercise string    `json:"exercise,omitempty"`
	Self     string    `json:"self,omitempty"`
	Tutor    string    `json:"tutor,omitempty"`
	Result   string    `json:"result,omitempty"`
	Notes    string    `json:"notes,omitempty"`
}

// Mock is one timed mock interview (Track D).
type Mock struct {
	At      time.Time `json:"at"`
	Format  string    `json:"format"` // hld-45 | lld-45 | mc-90 | loop
	Problem string    `json:"problem"`
	Minutes int       `json:"minutes"`
	Level   string    `json:"level"`
	Notes   string    `json:"notes,omitempty"`
}

// Review is the spaced-repetition state.
type Review struct {
	Cards map[string]*Card `json:"cards"`
	Log   []ReviewEntry    `json:"log"`
}

// Card is one quiz question enrolled in spaced review (Leitner boxes).
type Card struct {
	Module   string     `json:"module"`
	Box      int        `json:"box"`
	Due      string     `json:"due"` // YYYY-MM-DD, local date
	LastSeen *time.Time `json:"last_seen,omitempty"`
	Reps     int        `json:"reps"`
	Lapses   int        `json:"lapses"`
}

// ReviewEntry is one answered review card.
type ReviewEntry struct {
	At      time.Time `json:"at"`
	QID     string    `json:"qid"`
	Module  string    `json:"module"`
	Correct bool      `json:"correct"`
}

// EstimateHistory holds `sd estimate` attempts.
type EstimateHistory struct {
	Attempts []EstimateAttempt `json:"attempts"`
}

// EstimateAttempt is one answered estimation problem.
type EstimateAttempt struct {
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"`
	Expected float64   `json:"expected"`
	Given    float64   `json:"given"`
	Ratio    float64   `json:"ratio"`
	Within   bool      `json:"within"`
}

// Activity is a line in the activity log (used for recaps after a gap).
type Activity struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	Module string    `json:"module,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

// TutorNote is a free-form observation the tutor wants future sessions to see.
type TutorNote struct {
	At     time.Time `json:"at"`
	Module string    `json:"module,omitempty"`
	Tags   []string  `json:"tags,omitempty"`
	Note   string    `json:"note"`
}

// New returns an empty progress file.
func New() *Progress {
	p := &Progress{SchemaVersion: SchemaVersion}
	p.ensure()
	return p
}

func (p *Progress) ensure() {
	if p.Modules == nil {
		p.Modules = map[string]*ModuleRecord{}
	}
	if p.Review.Cards == nil {
		p.Review.Cards = map[string]*Card{}
	}
	if p.Activity == nil {
		p.Activity = []Activity{}
	}
	if p.TutorNotes == nil {
		p.TutorNotes = []TutorNote{}
	}
	if p.Review.Log == nil {
		p.Review.Log = []ReviewEntry{}
	}
	if p.Estimate.Attempts == nil {
		p.Estimate.Attempts = []EstimateAttempt{}
	}
}

// Load reads path; a missing file yields an empty Progress.
func Load(path string) (*Progress, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, err
	}
	p := &Progress{}
	if err := json.Unmarshal(b, p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if p.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("%s: schema_version %d is newer than this tool (%d); update the repo", path, p.SchemaVersion, SchemaVersion)
	}
	p.SchemaVersion = SchemaVersion
	p.ensure()
	return p, nil
}

// Save writes path atomically (temp file + rename) so a crash never truncates history.
func (p *Progress) Save(path string) error {
	p.ensure()
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".progress-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Module returns the record for id, creating it if needed.
func (p *Progress) Module(id string) *ModuleRecord {
	p.ensure()
	r, ok := p.Modules[id]
	if !ok {
		r = &ModuleRecord{}
		p.Modules[id] = r
	}
	return r
}

// Peek returns the record for id without creating it (nil if absent).
func (p *Progress) Peek(id string) *ModuleRecord { return p.Modules[id] }

// Touch records activity: appends to the log and bumps LastActiveAt.
func (p *Progress) Touch(now time.Time, kind, module, detail string) {
	p.ensure()
	t := now
	p.LastActiveAt = &t
	if p.Learner.StartedAt == nil {
		p.Learner.StartedAt = &t
	}
	p.Activity = append(p.Activity, Activity{At: now, Kind: kind, Module: module, Detail: detail})
	if len(p.Activity) > MaxActivity {
		p.Activity = p.Activity[len(p.Activity)-MaxActivity:]
	}
}

// GapDays is the number of whole days between the last recorded activity and now (0 if none).
func (p *Progress) GapDays(now time.Time) int {
	if p.LastActiveAt == nil {
		return 0
	}
	return DaysBetween(*p.LastActiveAt, now)
}

// HasActivity reports whether anything was recorded for the module, including
// lesson predictions and single-package lab attempts.
func (r *ModuleRecord) HasActivity() bool {
	return r != nil && (len(r.QuizAttempts)+len(r.LabRuns)+len(r.Designs)+len(r.Teachbacks)+len(r.Mocks)+
		len(r.Predictions)+len(r.LabAttempts) > 0)
}

// Attempted reports whether the lab package has been run at least once, either on its
// own or as part of a full lab run. The web UI shows the reference solution only then.
func (r *ModuleRecord) Attempted(pkg string) bool {
	if r == nil {
		return false
	}
	for _, a := range r.LabAttempts {
		if a.Package == pkg {
			return true
		}
	}
	for _, run := range r.LabRuns {
		for _, lp := range run.Packages {
			if lp.Package == pkg {
				return true
			}
		}
	}
	return false
}

// Predict saves the answer to a lesson's predict block. The first answer is kept: a
// prediction is only useful if it isn't edited after the reveal, so a second call
// returns the stored one with created=false.
func (p *Progress) Predict(module, id, answer string, now time.Time) (pred Prediction, created bool) {
	rec := p.Module(module)
	if old, ok := rec.Predictions[id]; ok {
		return *old, false
	}
	if rec.Predictions == nil {
		rec.Predictions = map[string]*Prediction{}
	}
	rec.Predictions[id] = &Prediction{At: now, Answer: answer}
	p.Touch(now, "predict", module, id)
	return *rec.Predictions[id], true
}

// Date is the local calendar date of t as YYYY-MM-DD.
func Date(t time.Time) string { return t.Local().Format("2006-01-02") }

// DaysBetween counts calendar days from a to b in local time.
func DaysBetween(a, b time.Time) int {
	da, _ := time.ParseInLocation("2006-01-02", Date(a), time.Local)
	db, _ := time.ParseInLocation("2006-01-02", Date(b), time.Local)
	return int(db.Sub(da).Hours()/24 + 0.5)
}

// AddDays returns the local date string n days after t.
func AddDays(t time.Time, n int) string { return Date(t.Local().AddDate(0, 0, n)) }

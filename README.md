# System Design Mastery Course (Go)

A hands-on, mastery-gated course for **senior-level system design interviews**: HLD, LLD/OOD and machine
coding. It's built for one learner: a backend engineer who can code and solve algorithm problems, but hasn't yet
seen *why* real systems are built the way they are. The goal is to understand systems well enough to build them,
not to memorise interview templates. Interviews are the forcing function.

Every concept follows the same loop:

1. **Failure story.** Something that works at 1× breaks at 10× or 100×, with real numbers.
2. **Predict.** You write down what you think happens or what you'd do, *before* you see the explanation.
3. **Reveal.** The mechanism from first principles, including why the tempting wrong answers are wrong.
4. **Build it.** A toy implementation in Go (stdlib only) with failing tests to make pass.
5. **Break it.** Fault injection: remove the lock, kill the backend, skew the clock, and watch what happens.
6. **In the wild.** Where the same mechanism lives in Redis, Kafka, Postgres, Envoy, the Go runtime…
7. **Senior bar.** Failure modes, capacity numbers, operations, cost, trade-off tables with "when the answer
   flips", and the follow-up probes interviewers actually ask.

Where it fits, lessons tie back to your own banking-platform work: integration adapters, vendor timeouts, duplicate
callbacks, SSO/OAuth, idempotent money movement, audit trails and multi-tenant credit unions.

## Start here

| File | What it is |
|---|---|
| [SYLLABUS.md](SYLLABUS.md) | Tracks A–E, every module with hours/prereqs/labs, the sequence, and the **definition of done** |
| [docs/INTERVIEW_FRAMEWORK.md](docs/INTERVIEW_FRAMEWORK.md) | The 45-minute HLD framework as a one-page card |
| [docs/NUMBERS.md](docs/NUMBERS.md) | Numbers to know (approximations flagged) |
| [modules/A0-framework-estimation](modules/A0-framework-estimation/) | First module: framework + estimation |
| [modules/B0-go-concurrency](modules/B0-go-concurrency/) | Run alongside A0: Go concurrency for interviews |
| [modules/A1-networking-api](modules/A1-networking-api/) | Unlocks after A0: networking, API design, DNS/LB/CDN |
| [TUTOR.md](TUTOR.md) | How a Claude chat session tutors you, critiques designs and runs mocks |
| [MODULE_TEMPLATE.md](MODULE_TEMPLATE.md) | The fixed format every module follows, plus the JSON schemas |

## Setup

You need **Go 1.22+** (stdlib only, no third-party modules) and a C toolchain for the race detector (`gcc` or
`clang`; `go env CGO_ENABLED` should print `1`).

Build the CLI into `./bin` (gitignored):

```bash
go build -o bin/sd ./cmd/sd
```

Put it on your PATH for the current shell:

```bash
export PATH="$PWD/bin:$PATH"
```

Check that everything is healthy:

```bash
./check.sh
```

`sd` finds the course by walking up from the current directory to the folder containing `course/course.json`. Set
`SD_ROOT` to run it from elsewhere. Your progress lives in `progress.json` at the repo root. **Commit it** at the
end of each session so your history survives across machines and cloud sessions.

## The `sd` CLI

| Command | What it does |
|---|---|
| `sd status` | What's unlocked, what's in progress, what's next, what needs a refresh, reviews due, estimation mastery |
| `sd status A1` | One module: gate checklist with the exact next action, units, labs |
| `sd progress` | Full history: quiz attempts, lab runs, tutor assessments, weakest tags, estimation accuracy by kind |
| `sd quiz A0` | A gated quiz attempt drawn from the module's question pool (least-seen questions first, choices shuffled) |
| `sd review` | Spaced repetition over every question you've met; misses come back tomorrow |
| `sd estimate -n 5` | Randomized back-of-envelope problems (`-kind qps\|storage\|bandwidth\|servers\|cache\|concurrency\|availability`) with worked solutions and a "so what" decision |
| `sd lab A0` | Runs the module's lab tests with `-race` and records the result for the gate |
| `sd recap` | After time away: what you were doing, open modules, weak tags, and a 5-question warm-up |
| `sd record …` | Used by the tutor to record design scores, teach-back verdicts, mocks and notes |
| `sd validate` | Checks course metadata, quizzes, lesson markup and lab/solution pairs (for authors) |
| `sd serve` | The same course in your browser at http://localhost:8080 (see below) |

`status`, `progress`, `recap` and `validate` accept `--json`. Everything the CLI shows comes from JSON files
(`course/course.json`, `modules/*/module.json`, `modules/*/quiz.json`, `progress.json`) and reusable Go packages
under `internal/`, and the web UI sits on exactly the same data.

## The web UI (`sd serve`)

```bash
sd serve
```

Then open http://localhost:8080. It listens on 127.0.0.1 only (`--port` picks another port), needs no network
(Markdown, Mermaid and syntax-highlighting libraries are embedded in the binary), and follows your system's light
or dark mode, with a toggle. It reads and writes the same `progress.json` as the CLI, so you can switch between the
two freely.

- **Roadmap:** every module by track (or as a dependency graph) with its state (locked, unlocked, in progress,
  passed, needs refresh) and the next recommended step. Modules authored in other sessions show up on reload.
- **Lessons:** rendered Markdown with Mermaid diagrams and highlighted Go. Each *Predict* block has an answer box;
  its reveal stays locked until you write an answer and click *Save & reveal*. The answer is saved in
  `progress.json` as first written, so you and the tutor can compare it with the reveal later.
- **Quizzes, review and recap:** the same gated draws, grading and spaced-repetition rules as `sd quiz` and
  `sd review`, with instant feedback, explanations and links to the section that teaches each question.
- **Estimation trainer:** randomized problems, tolerance check, worked solution and the "so what" decision.
- **Progress:** gate checklist per module, quiz history, the review queue, weak tags and estimation by kind.
- **Labs:** *Run tests* runs `go test -race` for one package and shows the output; the package's reference
  solution unlocks after your first run. *Run all core labs* is the same gate run as `sd lab`. (Single-package
  runs don't count for the gate.)

Design exercises and teach-backs are still assessed by the tutor and recorded with `sd record`.

## How a weekly unit works

Plan on ≈ 6 hours a week, usually three 2-hour sittings:

1. **Read & predict.** Work through the unit. At each *Predict* block, write your answer down (a notes file or a
   message to the tutor) before you open the reveal. Being wrong here is the point: it shows you where your model
   is off.
2. **Build & break.** Make the lab tests pass (`sd lab <ID>`), then do the *Break it* exercise.
3. **Retrieve & apply.** Answer *Check yourself*, then `sd review` (≈ 15 min), a few `sd estimate` problems, and the
   unit's share of the design exercise or teach-back.

Interleave tracks. A typical week is ≈ 4 h on the current Track A/C module and ≈ 2 h on Track B. `sd status`
suggests one next step per track for this reason.

## How mastery gating works

There are no deadlines. A module is **done** when its gate passes, and a gate passes only when **all** of these
hold:

- **Quiz:** ≥ 85% on two full `sd quiz` attempts **on different days**. One good day isn't mastery.
- **Labs:** every CORE lab package passes under `-race` (`sd lab`).
- **Design exercise:** you self-score against the rubric, the tutor critiques it, and the tutor's score is at
  least **meets** (the senior bar).
- **Teach-back:** you explain the concept and its trade-offs in your own words, and the tutor checks it against a
  must-hit checklist.

Passing a gate unlocks the modules that depend on it; timed mocks (Track D) unlock only after the relevant gates.
After you pass, `sd review` keeps bringing the module's questions back on a spaced schedule. If your recent review
accuracy for a module drops below 70%, it's marked **needs refresh** until you pass its quiz again. Nothing
re-locks; it just goes to the top of your to-do list.

## Pausing and resuming

You will take breaks; the course is built for it.

- Everything is in `progress.json`. Nothing depends on remembering where you were.
- `sd status` always shows the next action, and `sd status <ID>` shows exactly what's left on a gate.
- After **more than 14 days** away, `sd status` tells you to run `sd recap` first. That shows what you were doing,
  your open modules and weakest tags, and gives a short warm-up from what you last studied. The tutor also checks
  the gap and starts with a short recap conversation before any new material (see [TUTOR.md](TUTOR.md)).
- Reviews don't pile up into a wall: `sd review` serves at most 20 cards a session, most overdue first, so a few
  sessions clear the backlog.

## Working with the tutor

Open a Claude chat or Claude Code session in this repo and say "tutor me" (or "mock me" once Track D is unlocked).
[CLAUDE.md](CLAUDE.md) points the session at [TUTOR.md](TUTOR.md). The tutor reads `progress.json`, asks before it
tells, enforces the gates, critiques designs against the module rubrics, records results with `sd record`, and runs
timed mocks with realistic follow-up probes.

## Repository layout

```
course/course.json         tracks, policy thresholds, recommended order
modules/<ID>-<slug>/       module.json + README, unit lessons, design exercise, teach-back, quiz.json
labs/<id>/<pkg>/           stubs with failing tests: your work goes here
solutions/<id>/<pkg>/      reference solutions (same tests); look only after a real attempt
_mocks/<date>-<format>-…/  your Track D mock logs and mock code (outside ./..., committed with progress.json)
docs/                      interview framework card, numbers sheet
cmd/sd/                    the CLI (terminal I/O only)
internal/                  course loading/validation, progress, mastery gates, quiz, review, estimate, lab runner
internal/web/              `sd serve`: JSON API + embedded single-page app (static/, vendored JS in static/vendor/)
web/                       npm manifest that pins and vendors the browser libraries (not needed to build or run)
check.sh                   repo health check (solutions pass, labs fail, tests identical, vet, validate)
```

## Authoring more modules

Modules other than A0, A1 and B0 exist as `module.json` metadata with `"status": "planned"`. To write one, follow
[MODULE_TEMPLATE.md](MODULE_TEMPLATE.md) exactly and use A0, A1 and B0 as the quality bar. `sd validate` and
`./check.sh` must pass before a module is flipped to `"written"`. Several sessions can author modules in parallel,
because each one only touches its own `modules/<ID>-*`, `labs/<id>` and `solutions/<id>` directories.

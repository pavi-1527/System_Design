# Module template (the contract every module follows)

This file is the format spec for every module in the course. Modules A0, A1 and B0 are the worked examples. When
you write a new module (by hand or in a parallel Claude session), copy their *structure* exactly. Content depth may
vary; the shape may not. At the end of this file there is a quality checklist. A module is not done until every box
in it is ticked.

**Everything is UI-agnostic.** The `sd` CLI and the local web UI (`sd serve`) read the *same* files. So: metadata and quizzes are JSON with the schemas below, lessons are plain Markdown with
machine-detectable predict/reveal markup, and nothing may be written so that it only works in a terminal. Logic lives
in `internal/...` packages (pure functions over these schemas); `cmd/sd` only does terminal I/O.

---

## 1. Files and where they go

For module `<ID>` (e.g. `A3`) with slug `<slug>` (e.g. `caching`):

```
modules/<ID>-<slug>/
  module.json               # metadata read by the CLI and any UI (schema in §8)
  README.md                 # module overview, unit plan, mastery gate checklist
  unit-1-<topic>.md         # one file per ~6h weekly unit
  unit-2-<topic>.md
  ...
  design-exercise.md        # the gated design exercise + rubric
  teachback.md              # the gated teach-back prompt + evaluator checklist
  quiz.json                 # question pool used by `sd quiz <ID>` and `sd review`
labs/<id>/                  # lowercase id, e.g. labs/a3/
  README.md                 # what each lab is, order, how to run, break-it exercises
  <pkg>/<pkg>.go            # stub: compiles, exported API + doc comments, TODO bodies
  <pkg>/<pkg>_test.go       # tests (MUST be byte-identical to the solutions copy)
solutions/<id>/
  <pkg>/<pkg>.go            # reference solution
  <pkg>/<pkg>_test.go       # identical copy of the lab test
```

Every module — including planned, unwritten ones — has a `module.json`; the CLI discovers modules by globbing
`modules/*/module.json`. When you author a planned module, keep its id, slug, prerequisites and gate unless there is a
reason to change them (then update SYLLABUS.md too), fill in units/labs/outcomes, and flip `status` to `"written"`.
`sd validate` checks most of this mechanically; `./check.sh` checks the lab/solution contract.

## 2. `README.md` (module overview)

Use exactly these headings, in this order:

```markdown
# <ID> — <Title>

| | |
|---|---|
| Track | A — HLD fundamentals |
| Estimated time | 18 h (3 weekly units) — CORE 15 h, STRETCH 3 h |
| Prerequisites | A0 (gate passed) |
| Labs | `labs/<id>/` — list of packages |
| Gate | quiz ≥ 85% ×2 (different days) · labs pass · design ≥ meets · teach-back pass |

## Why this module exists
<A short failure story: a concrete system that works at 1x and breaks at 10x/100x, told so the learner feels the
pain before seeing any solution. 1–3 paragraphs. No definitions yet.>

## Outcomes
<5–10 bullets, each starting with a verb you can test: "Estimate…", "Choose between X and Y given…",
"Implement…", "Explain why… and when the answer flips". Mark each CORE or STRETCH.>

## Unit plan
| Unit | Topic | Hours | Core/Stretch | Lab |
|---|---|---|---|---|
| 1 | ... | 6 | CORE (5) / STRETCH (1) | `labs/<id>/<pkg>` |

<Then a per-unit session breakdown (e.g. "Session 1 (2h): read §1–§2, predict boxes, lab part 1"), so a learner can
plan a week around 3 × 2h sittings.>

## How this connects to your banking work
<2–5 bullets linking concepts to the learner's own experience: integration adapters, vendor failures, SSO/OAuth,
idempotency, retries, audit trails, multi-tenancy. Be specific, not generic.>

## Mastery gate checklist
- [ ] `sd quiz <ID>` ≥ 85% — attempt 1 (date: ____)
- [ ] `sd quiz <ID>` ≥ 85% — attempt 2 on a different day (date: ____)
- [ ] `sd lab <ID>` — all lab tests pass (with `-race`)
- [ ] Design exercise (`design-exercise.md`): self-score recorded, tutor score ≥ "meets"
      (`sd record design <ID> --self <lvl> --tutor <lvl> --notes "..."`)
- [ ] Teach-back (`teachback.md`): tutor marks pass (`sd record teachback <ID> --result pass --notes "..."`)

`sd status` is the source of truth; this list is for your own reading.

## What's next
<Which modules this unlocks, and what to review via `sd review` before moving on.>
```

## 3. Unit files (`unit-N-<topic>.md`)

A unit is ~6 hours of work: reading, predicting, building, breaking. Structure:

```markdown
# <ID> Unit N — <Topic>

> Time: ~6 h · CORE: §1–§4 · STRETCH: §5 · Lab: `labs/<id>/<pkg>`

## Before you start
<What you should already be able to do (link to prior units), and a 3-question warm-up from earlier material
(answers in `<details data-kind="answer">`). This doubles as spaced retrieval.>

## §1 <Concept name>
### The failure story
### Predict
### Reveal
### How it actually works
### Build it
### Break it
### In the wild
### Trade-offs
### Senior bar
### Check yourself

## §2 ...

## Unit wrap-up
<Summary table of the unit's decisions ("use X when…, flip to Y when…"), and a pointer to the quiz/lab.>
```

Every concept section (`§`) has these sub-sections, in order. Section-specific rules:

| Sub-section | Rule |
|---|---|
| **The failure story** | Concrete scenario, concrete numbers, what goes wrong at 10x/100x. No solution yet. |
| **Predict** | 1–3 questions inside a predict block (markup below). Instruct the learner to *write the answer down* (in their notes or to the tutor) before opening the reveal. Questions must require reasoning, not recall. |
| **Reveal** | A `<details data-kind="reveal" data-for="<predict id>">` block right after the predict block. Explains the right answer *and* the tempting wrong ones. |
| **How it actually works** | The mechanism, from first principles. Mermaid diagram(s) where a picture helps. Show the internals, not just the box. |
| **Build it** | Points at a lab package and the exact functions to implement; states the acceptance tests. Omit only if the section is purely conceptual (then say "No lab for this section — see §X"). |
| **Break it** | A fault-injection exercise: change a parameter, remove a lock, kill a backend, add skew. State what you expect to observe and how to observe it (test, log, `-race`, metric). Answer in `<details data-kind="answer">`. |
| **In the wild** | Where this exists in real systems (Redis, Kafka, Postgres, Envoy, NGINX, Linux, etc.) with specifics. Unsure facts get `TODO-VERIFY`. |
| **Trade-offs** | A table: option, wins when, loses when, **when the answer flips**. At least two options. |
| **Senior bar** | Failure modes, capacity numbers (marked `≈` when approximate), operational concerns (deploys, migrations, on-call), cost. Plus 2–4 **interviewer follow-up probes** with model answers in `<details>`. |
| **Check yourself** | 2–4 short questions with answers in `<details data-kind="answer">`. These are *not* the quiz; the quiz lives in `quiz.json`. |


### Predict / reveal markup (machine-detectable — exact syntax)

```markdown
<!-- predict id="A1-U2-P03" -->
**Predict.** Your API returns 20 items per page with `?offset=`. A user is on page 3 while 5 new items are
inserted at the top. What do they see on page 4? Write your answer down before opening the reveal.
<!-- /predict -->

<details data-kind="reveal" data-for="A1-U2-P03">
<summary>Reveal</summary>

They see 5 items again that were already on page 3 ...

</details>
```

Rules (checked by `sd validate`):
- Predict ids are `<ID>-U<unit>-P<NN>` (README/design/teach-back files may use `<ID>-U0-P<NN>`), unique within the
  module. Each predict block has **exactly one** reveal with a matching `data-for`, placed after it in the same file.
- **Every** `<details>` in a module carries `data-kind`: `reveal` (answers a predict block, needs `data-for`),
  `answer` (warm-ups, check-yourself, break-it answers, probe model answers) or `hint`.
- Leave a blank line after `<summary>…</summary>` and before `</details>` so the Markdown inside renders.
- The markers are HTML comments and attributes, so the file still reads normally on GitHub, in an editor, or in a
  browser; a UI can find the blocks with simple regexes (see `internal/course/validate.go`, `ScanMarkup`).
- Every unit file contains at least one predict block.

### Style rules
- Peer-to-peer tone. No "In today's world…", no hype, no filler. Short paragraphs.
- Why before what: never introduce a mechanism before the reader knows what breaks without it.
- Diagrams in Mermaid (` ```mermaid `). Keep them small; one idea per diagram.
- Go code in docs: compile-able snippets, Go 1.22+, stdlib only.
- Numbers: mark approximate figures with `≈` or "approximately". Cite the source kind ("vendor docs", "measured on
  commodity hardware, varies") when it matters. If you are not sure, write `TODO-VERIFY: <claim>`; never guess.
  Prefer numbers that already appear in `docs/NUMBERS.md` and stay consistent with it.
- Link the learner's banking-adapter experience where it genuinely fits; don't force it.
- UI-agnostic: write "run `sd quiz A1`" (a command any front end can map), never "press Enter" or other
  terminal-only instructions. Paths in prose are repo-relative links.

## 4. Labs

Rules (enforced by `./check.sh` and `sd validate`):

1. One Go package per lab under `labs/<id>/<pkg>/`, with the matching reference solution under
   `solutions/<id>/<pkg>/`. Same package name, same exported API.
2. Every `*_test.go` file in a lab package is **byte-identical** to the one in the solution package.
3. Stubs **compile** and pass `go vet`. Bodies are `// TODO(lab): ...` with zero-value / `errNotImplemented`
   returns. Never `panic` in a stub (one panic hides every other test result), and never block forever (a stub
   that hangs is a terrible first experience): return immediately.
4. `go test ./labs/<id>/...` **fails** on the stubs; `go test -race ./solutions/<id>/...` **passes**.
5. Tests that involve goroutines must not hang if the implementation is wrong: use `context.WithTimeout`,
   `time.After`, or a helper that fails the test after a deadline. Mark concurrency tests so `-race` exercises
   them (concurrent callers, not just sequential ones).
6. stdlib only. No third-party modules, ever.
7. Test names describe behaviour (`TestIdempotency_ConcurrentDuplicateExecutesOnce`), and failure messages say
   what was expected and why it matters ("handler ran 2 times; a retried payment would be charged twice").
8. Optional fault-injection code for "Break it" exercises lives behind a build tag
   (`//go:build breakit`) so default runs ignore it. Document the exact command in `labs/<id>/README.md`.
9. `labs/<id>/README.md` lists packages in suggested order with: goal, files to edit, run command
   (`go test -race ./labs/<id>/<pkg>/`), and the break-it exercises.

## 5. Quiz (`quiz.json`)

Quizzes are JSON only (no Markdown quizzes), so every front end grades them identically via `internal/quiz`.
Schema (validated strictly by `sd validate`; unknown fields are errors):

```json
{
  "module": "A1",
  "title": "Networking & API design",
  "draw": 15,
  "questions": [
    {
      "id": "A1-001",
      "type": "single",
      "tags": ["pagination"],
      "difficulty": 2,
      "prompt": "Text of the question. Plain text; use \\n for line breaks.",
      "choices": ["option A", "option B", "option C", "option D"],
      "answer": [1],
      "explanation": "Why B is right AND why A, C, D are wrong.",
      "ref": "unit-2-api-design.md#2-pagination"
    },
    {
      "id": "A1-002",
      "type": "multi",
      "tags": ["idempotency"],
      "difficulty": 2,
      "prompt": "Select ALL that apply: ...",
      "choices": ["...", "...", "...", "..."],
      "answer": [0, 2],
      "explanation": "...",
      "ref": "..."
    },
    {
      "id": "A1-003",
      "type": "numeric",
      "tags": ["estimation"],
      "difficulty": 2,
      "prompt": "... Answer in requests/second.",
      "value": 1150,
      "tolerance": 0.25,
      "unit": "req/s",
      "explanation": "Worked solution, step by step.",
      "ref": "..."
    }
  ]
}
```

| Field | Type | Required | Rules |
|---|---|---|---|
| `module` | string | yes | equals the module's `id` |
| `title` | string | yes | display title |
| `draw` | int | no | questions per attempt; defaults to `policy.default_quiz_draw` (15); pool must be ≥ 2 × `draw` |
| `questions` | array | yes | the pool |
| `questions[].id` | string | yes | `<ID>-NNN`, unique, never reused |
| `questions[].type` | string | yes | `single`, `multi` or `numeric` |
| `questions[].tags` | [string] | yes | ≥ 1; short lowercase tags, reused across modules |
| `questions[].difficulty` | int | no | 1 (recall) – 3 (senior probe) |
| `questions[].prompt` | string | yes | plain text, `\n` for line breaks; code is fine (lines ≤ 90 chars keep their spacing) |
| `questions[].choices` | [string] | single/multi | ≥ 2; shuffled at runtime |
| `questions[].answer` | [int] | single/multi | 0-based indices into `choices`; exactly 1 for `single`, ≥ 1 for `multi`, no repeats |
| `questions[].value` | number | numeric | non-zero, expressed in `unit` |
| `questions[].tolerance` | number | numeric | relative, in (0, 1]; 0.25 = ±25% |
| `questions[].unit` | string | no | parsed by `internal/quantity`: byte sizes (`B`, `KB`…`PB`, `KiB`…), bit rates (`bps`, `Mbps`, `Gbps`), durations (`ms`, `s`, `min`, `hours`, `days`); anything else (`req/s`, `servers`) is a count label. A bare number typed by the learner is read in this unit; an explicit unit (`0.04 PB`) is converted |
| `questions[].explanation` | string | yes | why the answer is right and why each distractor is wrong |
| `questions[].ref` | string | yes | module-dir-relative file, optional `#anchor` (GitHub heading slug) |

Rules:
- `id` is `<ID>-NNN`, unique, **never renumbered or reused** (spaced-repetition history is keyed on it). Retire a
  question by deleting it; add new ones with new ids.
- `type` ∈ `single` | `multi` | `numeric`. `answer` holds 0-based choice indices. `numeric` uses `value` and a
  relative `tolerance` (0.25 = ±25%). Numeric input accepts suffixes like `1.2k`, `3M`, `2e6`.
- `draw`: how many questions one `sd quiz` attempt samples (default 15). The pool must be ≥ 2× `draw` so two
  attempts on different days see mostly different questions.
- Choices are shuffled at runtime: never write "all of the above", "both A and B", or refer to positions.
- At least 40% of questions are scenario questions ("a vendor times out at p99 = 8s; which…"), not definitions.
- Every `explanation` explains the distractors too. Every question has a `ref` to the section that teaches it.
- Cover every CORE outcome with ≥ 2 questions. Tags are short, lowercase, reused across modules where possible
  (`estimation`, `caching`, `idempotency`, `consistency`, `concurrency`, ...) because `sd progress` reports
  weakness by tag.

## 6. Design exercise (`design-exercise.md`)

```markdown
# <ID> Design exercise — <name>

> Time box: <N> minutes, solo, no notes. Then self-score, then send to the tutor.

## Prompt
<The problem as an interviewer would state it; deliberately underspecified where clarifying questions matter.>

## What to produce
<Exactly what artifacts: requirements list, estimate sheet, API sketch, diagram, trade-off table, ...>

## Rubric
| Dimension | below | approaching | meets (senior bar) | exceeds |
|---|---|---|---|---|
| ... | ... | ... | ... | ... |

## Tutor critique protocol
<What the tutor checks, the follow-up probes they must ask, and what "meets" requires overall
(e.g. "meets" on every dimension, or "meets" overall with no dimension at "below").>

## Recording
`sd record design <ID> --self <below|approaching|meets|exceeds> --tutor <level> --notes "<one-line summary>"`
```

Scale meanings, used course-wide:
- **below** — missing or wrong; would fail a mid-level interview.
- **approaching** — right direction, but shallow or needed heavy prompting.
- **meets** — senior bar: driven by the candidate, quantified, trade-offs explicit, failure modes covered.
- **exceeds** — staff signal: anticipates the interviewer, reasons about evolution/cost/org impact.

## 7. Teach-back (`teachback.md`)

```markdown
# <ID> Teach-back

## Prompt
<What to explain, to whom (e.g. "a mid-level engineer on your team who has never seen this"), and in what form
(written, 400–800 words, own words, may include one diagram).>

## Evaluator checklist
Must-hit (all required for pass):
- [ ] ...
Should-hit (at least N of M):
- [ ] ...
Red flags (any one = revise):
- [ ] ...

## Recording
`sd record teachback <ID> --result <pass|revise> --notes "<missed items>"`
```

## 7b. Track D (mock) modules

Mock modules gate on `mocks` only, so they have no `quiz.json`, design exercise, teach-back or labs. Instead each one
is the *kit* for its interview format, which the tutor uses to run, time and score the mock (see TUTOR.md §8):

```
modules/D<n>-<slug>/
  module.json        # gate: {"requires": ["mocks"], "mocks_required": N, "mock_formats": ["<format>"]}
  README.md          # the §2 headings; the gate checklist lists the N mocks and their `sd record mock` lines
  unit-N-<topic>.md  # how to perform in this format (§3 structure, predict blocks required); hours include the mocks
  format.md          # the timed format: phases, time checkpoints, behind-schedule rules, the interviewer's script
  rubric.md          # the senior-level rubric for the format: dimensions × 4 levels, gating dimensions, overall rule
  probe-bank.md      # interviewer follow-up probes (D3: per case study), each with what a "meets" answer contains
  problem-bank.md    # interviewer-only prompts with answer sheets and the mid-way change (a spoiler for the learner)
  mock-log.md        # the template the learner copies to _mocks/<YYYY-MM-DD>-<format>-<slug>/log.md per mock
```

D4 (the full loop) reuses the D1–D3 rubrics and banks and adds the loop schedule, the loop scoring rule and the
component oral. Formats: `lld-45` (D1), `mc-90` (D2), `hld-45` (D3), `loop` (D4). Filled-in mock logs and
machine-coding code live under `_mocks/` at the repo root: the leading underscore keeps them out of `./...`, so
`./check.sh` and `go vet ./...` never pick up work in progress. They are committed with `progress.json`.

## 8. Module metadata (`modules/<ID>-<slug>/module.json`)

One per module, planned or written. Paths are **repo-root-relative**. Read by `internal/course` (Go types in
`internal/course/course.go`); validated strictly by `sd validate`.

```json
{
  "schema_version": 1,
  "id": "A1",
  "slug": "networking-api",
  "title": "Networking, API design, DNS/LB/CDN",
  "track": "A",
  "status": "written",
  "core": true,
  "summary": "One line shown in listings.",
  "hours": {"core": 15, "stretch": 3, "total": 18},
  "prerequisites": ["A0"],
  "gate": {"requires": ["quiz", "lab", "design", "teachback"]},
  "paths": {
    "dir": "modules/A1-networking-api",
    "readme": "modules/A1-networking-api/README.md",
    "quiz": "modules/A1-networking-api/quiz.json",
    "design_exercise": "modules/A1-networking-api/design-exercise.md",
    "teachback": "modules/A1-networking-api/teachback.md",
    "lab": "labs/a1",
    "solution": "solutions/a1"
  },
  "units": [
    {"n": 1, "title": "The request path", "file": "modules/A1-networking-api/unit-1-request-path.md",
     "hours": {"core": 5, "stretch": 1}, "labs": ["lb"]}
  ],
  "labs": [
    {"package": "lb", "title": "Load balancer pickers + passive health", "unit": 1, "core": true}
  ],
  "outcomes": [{"text": "Choose between L4 and L7 load balancing given ...", "core": true}]
}
```

| Field | Type | Rules |
|---|---|---|
| `schema_version` | int | `1` |
| `id` | string | `<Track><n>`, e.g. `A1`, `C12`; must start with `track`; directory is `modules/<id>-<slug>` |
| `slug` | string | kebab-case |
| `track` | string | one of the ids in `course/course.json` → `tracks` (A–E) |
| `status` | string | `planned` (metadata only) or `written` (all paths must exist) |
| `core` | bool | `false` = STRETCH module: not required for the course definition of done |
| `hours` | object | `core` + `stretch` (= `total` if given); for written modules, must equal the sum over `units` |
| `prerequisites` | [id] | gates that must pass before this module unlocks; no cycles |
| `gate.requires` | [string] | subset of `quiz`, `lab`, `design`, `teachback`, `mocks` |
| `gate.mocks_required` | int | required when `mocks` is in `requires`: mocks at ≥ `mock_min_level` |
| `gate.mock_formats` | [string] | optional, only with `mocks`: the formats that count (`hld-45`, `lld-45`, `mc-90`, `loop`); `sd record mock` refuses any other format for this module; empty = any format |
| `paths.*` | string | `quiz`/`design_exercise`/`teachback` required if the gate needs them; `lab`/`solution` if it needs `lab` |
| `units[]` | array | `n`, `title`, `file`, `hours{core,stretch}`, `labs` (package names from `labs[]`) |
| `labs[]` | array | `package` (dir under `paths.lab` and `paths.solution`), `title`, `unit` (0 = TBD for planned), `core` (only core labs gate) |
| `outcomes[]` | array | `text`, `core` |

### Course settings (`course/course.json`)

Read by `internal/course` (`Course`, `Track`, `Policy` in `internal/course/course.go`); validated strictly.

```json
{
  "schema_version": 1,
  "title": "System Design Mastery Course (Go)",
  "tracks": [{"id": "A", "title": "HLD fundamentals"}],
  "policy": {
    "quiz_pass_threshold": 0.85,
    "quiz_passes_required": 2,
    "quiz_passes_distinct_days": true,
    "default_quiz_draw": 15,
    "rubric_levels": ["below", "approaching", "meets", "exceeds"],
    "design_min_level": "meets",
    "mock_min_level": "meets",
    "mock_distinct_problems": true,
    "review_intervals_days": [1, 3, 7, 14, 30, 60, 120],
    "review_session_size": 20,
    "review_refresh_threshold": 0.70,
    "review_refresh_window": 10,
    "review_refresh_min_items": 5,
    "review_overdue_days": 45,
    "recap_gap_days": 14,
    "estimate_tolerance": 0.25,
    "estimate_mastery_window": 20,
    "estimate_mastery_rate": 0.80
  },
  "recommended_order": ["A0", "B0", "A1"]
}
```

| Field | Type | Meaning |
|---|---|---|
| `tracks[]` | `{id, title}` | valid values for a module's `track` |
| `recommended_order` | [id] | every module id once; drives "next" suggestions |
| `policy.quiz_pass_threshold` | number | score a quiz attempt needs to count as a pass |
| `policy.quiz_passes_required` | int | passing full-length attempts needed |
| `policy.quiz_passes_distinct_days` | bool | passes must fall on different local days |
| `policy.default_quiz_draw` | int | questions per attempt when a quiz has no `draw` |
| `policy.rubric_levels` | [string] | ordered worst → best |
| `policy.design_min_level` / `mock_min_level` | string | minimum tutor level for the design / mock components |
| `policy.mock_distinct_problems` | bool | when true, only the first passing mock per problem (case- and space-insensitive name) counts towards a mock gate; default `false` when the key is missing |
| `policy.review_intervals_days` | [int] | Leitner box intervals |
| `policy.review_session_size` | int | max cards per `sd review` |
| `policy.review_refresh_threshold` / `_window` / `_min_items` | number / int / int | flag needs-refresh when accuracy over the last `window` reviews (≥ `min_items`) is below `threshold` |
| `policy.review_overdue_days` | int | passed module with no review for this long is "review overdue" |
| `policy.recap_gap_days` | int | a gap of more than this many days triggers the recap |
| `policy.estimate_tolerance` | number | relative tolerance for `sd estimate` (only this tolerance counts toward mastery) |
| `policy.estimate_mastery_window` / `_rate` | int / number | mastery = ≥ `rate` within tolerance over the last `window` drills, all kinds covered |

Missing policy keys fall back to the defaults shown. Change policy here, never in code.

## 8b. Learner state (`progress.json`)

Written only through `internal/progress` + `internal/mastery` (the CLI's `sd quiz/lab/record/review/estimate`
commands and the same actions in `sd serve`), read by everything. Saved atomically (temp file + rename). Timestamps are RFC 3339 with offset; "days"
are the learner's local calendar days. Commit it so progress survives across machines and cloud sessions.

```json
{
  "schema_version": 1,
  "learner": {"name": "", "started_at": "2026-10-28T19:02:11+05:30"},
  "last_active_at": "2026-10-30T21:40:00+05:30",
  "modules": {
    "A0": {
      "quiz_attempts": [
        {"at": "…", "correct": 13, "total": 15, "score": 0.8667,
         "answers": [{"qid": "A0-004", "correct": true, "given": "2) …"}]}
      ],
      "lab_runs": [
        {"at": "…", "passed": true, "race": true,
         "packages": [{"package": "capacity", "core": true, "passed": true, "failed_tests": 0}]}
      ],
      "designs": [
        {"at": "…", "exercise": "modules/A0-…/design-exercise.md", "self": "meets", "tutor": "approaching",
         "notes": "estimates not tied to decisions"}
      ],
      "teachbacks": [{"at": "…", "result": "revise", "notes": "missed: why estimation precedes API"}],
      "mocks": [{"at": "…", "format": "hld-45", "problem": "news feed", "minutes": 45, "level": "meets", "notes": "…"}],
      "predictions": {"A0-U1-P01": {"at": "…", "answer": "requirements → estimates → API → …"}},
      "lab_attempts": [{"at": "…", "package": "capacity", "passed": false, "race": true, "failed_tests": 3}],
      "gate_passed_at": "…",
      "needs_refresh": false,
      "refresh_reason": "",
      "refresh_flagged_at": "…",
      "refreshed_at": "…"
    }
  },
  "review": {
    "cards": {"A0-004": {"module": "A0", "box": 2, "due": "2026-11-06", "last_seen": "…", "reps": 3, "lapses": 1}},
    "log": [{"at": "…", "qid": "A0-004", "module": "A0", "correct": true}]
  },
  "estimate": {"attempts": [{"at": "…", "kind": "qps", "expected": 1157.4, "given": 1200, "ratio": 1.04, "within": true}]},
  "activity": [{"at": "…", "kind": "quiz", "module": "A0", "detail": "13/15 (87%)"}],
  "tutor_notes": [{"at": "…", "module": "A0", "tags": ["estimation"], "note": "drops the peak factor under time pressure"}]
}
```

Field reference (Go types in `internal/progress/progress.go`). Optional timestamps are omitted until set.

| Field | Type | Meaning |
|---|---|---|
| `schema_version` | int | `1`; newer files are rejected by older tools |
| `learner.name`, `learner.started_at` | string, time | `started_at` = first recorded activity |
| `last_active_at` | time | last learning action (status/progress views don't bump it) |
| `modules.<ID>.quiz_attempts[]` | object | `at`, `correct`, `total`, `score` (0–1), `answers[]` of `{qid, correct, given}`; full-length gated attempts only |
| `modules.<ID>.lab_runs[]` | object | `at`, `passed` (all core packages), `race`, `packages[]` of `{package, core, passed, failed_tests}` |
| `modules.<ID>.designs[]` | object | `at`, `exercise`, `self`, `tutor` (rubric levels), `notes`; the latest one counts |
| `modules.<ID>.teachbacks[]` | object | `at`, `result` (`pass`/`revise`), `notes`; the latest one counts |
| `modules.<ID>.mocks[]` | object | `at`, `format` (`hld-45`/`lld-45`/`mc-90`/`loop`), `problem`, `minutes`, `level`, `notes` |
| `modules.<ID>.predictions.<predict id>` | object | `at`, `answer`: the learner's written answer to a lesson predict block, saved by `sd serve` when they reveal it; kept as first written, never gating |
| `modules.<ID>.lab_attempts[]` | object | `at`, `package`, `passed`, `race`, `failed_tests`: single-package runs from `sd serve`'s "Run tests"; they unlock that package's solution in the UI but never gate |
| `modules.<ID>.gate_passed_at` | time | stamped once, never cleared |
| `modules.<ID>.needs_refresh`, `refresh_reason`, `refresh_flagged_at`, `refreshed_at` | bool, string, time, time | refresh state |
| `review.cards.<qid>` | object | `module`, `box` (index into `review_intervals_days`), `due` (`YYYY-MM-DD` local), `last_seen`, `reps`, `lapses` |
| `review.log[]` | object | `at`, `qid`, `module`, `correct`; every review answer, plus recap misses |
| `estimate.attempts[]` | object | `at`, `kind`, `expected` and `given` in **base units** (count, bytes, bits/s or seconds), `ratio` = given/expected, `within` |
| `activity[]` | object | `at`, `kind` (`quiz`, `quiz_practice`, `lab`, `lab_attempt`, `design`, `teachback`, `mock`, `review`, `recap`, `estimate`, `predict`, `note`, `gate_passed`, `needs_refresh`), `module`, `detail`; capped at 500 |
| `tutor_notes[]` | object | `at`, `module`, `tags`, `note` |

Semantics (implemented in `internal/mastery`; a UI must call it rather than re-implement it):
- **Quiz component** met when ≥ `quiz_passes_required` full-length attempts score ≥ `quiz_pass_threshold` on
  distinct local days. Shorter `-n` runs are practice: they feed the review deck but not `quiz_attempts`.
- **Lab** met when the latest recorded run passed every core package (runs with the race detector deliberately
  disabled are not recorded).
- **Design** met when the latest assessment has a self-score and a tutor score ≥ `design_min_level`. A tutor score
  recorded the same day as a self-only record merges into it.
- **Teach-back** met when the latest verdict is `pass`. **Mocks** met with ≥ `mocks_required` mocks at ≥ `mock_min_level`, counting only formats in
  `gate.mock_formats` and, with `mock_distinct_problems`, one mock per problem. A mock can only be recorded once the
  module is unlocked (every prerequisite's gate passed) and in a format the module accepts.
- `gate_passed_at` is stamped the first time all components hold and is never cleared; later regressions show up
  as `needs_refresh` instead (set when the last 10 reviews since the last refresh fall below 70%; cleared by a
  passing full quiz). Refresh never re-locks dependents.
- Review cards: Leitner boxes over `review_intervals_days`; a quiz answer enrolls a new card (box 1 if correct,
  box 0 if missed); a miss anywhere resets to box 0; only reviews promote.
- A module is **in progress** once anything is recorded for it, including predictions and lab attempts; neither
  of those counts towards its gate.
- `activity` is capped at 500 entries; `last_active_at` drives the recap-after-gap rule.

## 9. Quality checklist (tick all before marking a module "written")

Content
- [ ] Every § starts with a failure story with concrete numbers, before any solution.
- [ ] Every § has a Predict block using the exact predict/reveal markup; every `<details>` has a `data-kind`.
- [ ] Every § has Trade-offs (with "when the answer flips") and Senior bar (failure modes, capacity numbers,
      ops, cost, follow-up probes).
- [ ] Every component that can be built has a Go lab with a Break-it exercise.
- [ ] "In the wild" names real systems specifically; no invented facts. Unsure claims are `TODO-VERIFY`.
- [ ] Approximate numbers are marked `≈`; consistent with `docs/NUMBERS.md`.
- [ ] At least one banking-experience link per unit where it fits naturally.
- [ ] CORE vs STRETCH is labelled on every unit and § that is optional.
- [ ] Hour estimates in README add up to `module.json` `hours`, which equal the sum of unit hours.
- [ ] Nothing is terminal-only; lessons read correctly in a browser.

Labs
- [ ] `./check.sh` passes (solutions pass with `-race`, labs fail, test files identical, `go vet` clean).
- [ ] Stubs return immediately; no panics, no hangs.
- [ ] Tests include concurrent use where the component would be used concurrently.

Assessment
- [ ] `quiz.json` pool ≥ 2 × draw, ≥ 40% scenario questions, every explanation covers distractors.
- [ ] `sd validate` passes.
- [ ] Design exercise has a 4-level rubric and a tutor protocol with probes.
- [ ] Teach-back has must-hit / should-hit / red-flag lists.
- [ ] README gate checklist matches `module.json` `gate.requires`; `status` flipped to `"written"`.
- [ ] Track D only: the §7b kit files exist, `gate.mock_formats` names the module's format, and no example in a
      learner-facing file (units, README, format, rubric, probe bank, mock log) is a problem from any D problem bank.

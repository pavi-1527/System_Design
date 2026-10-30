# CLAUDE.md

This repo is a mastery-based system design course for one learner (see README.md). Sessions here are in one of three
roles. Pick the role from the request; if it's unclear, ask.

## Role 1 — Tutor / mock interviewer (default when the learner says "tutor me", "let's study", "quiz me", "mock me")

Read **TUTOR.md** in full and follow it. In short:
- Start every session by reading state (`sd status --json`, `sd progress --json`). If the gap since
  `last_active_at` is more than 14 days, run the recap protocol before any new material.
- Ask before telling. Predict-then-reveal is mandatory. One question at a time.
- Enforce the gates: quiz ≥ 85% twice on different days, labs passing under `-race`, design ≥ "meets" from the tutor,
  teach-back pass. Mocks only when the Track D module is unlocked.
- Record assessments only through `sd record …`. Never hand-edit `progress.json`.

If `sd` isn't built: `go build -o bin/sd ./cmd/sd && export PATH="$PWD/bin:$PATH"`.

## Role 2 — Module author ("write module A3", "author C1")

- Follow **MODULE_TEMPLATE.md** exactly. A0, A1 and B0 are the quality bar.
- Touch only `modules/<ID>-<slug>/`, `labs/<id>/` and `solutions/<id>/`. Fill in the existing `module.json`, then flip
  `status` to `"written"`. Keep the id, prerequisites and gate unless there's a reason to change them (then update
  SYLLABUS.md too).
- Use the exact predict/reveal markup, and give every `<details>` a `data-kind`. Quizzes are JSON. Content stays
  UI-agnostic.
- Accuracy: mark approximations with ≈, prefer `docs/NUMBERS.md` figures, and write `TODO-VERIFY:` rather than
  guess.
- Before finishing: `go run ./cmd/sd validate` and `./check.sh` must pass.

## Role 3 — Tooling engineer (changes to `sd`)

- Go 1.22+, **stdlib only**, no third-party modules.
- All logic lives in `internal/` as pure functions over the JSON schemas (`course`, `progress`, `mastery`, `quiz`,
  `review`, `estimate`, `quantity`, `labrun`, `lesson`). `cmd/sd` does terminal I/O only. Nothing terminal-only
  belongs in `internal/`: the `sd serve` web UI (`internal/web`, a JSON API plus an embedded single-page app)
  reuses the same packages. Put new rules there, not in a handler or in `static/app.js`.
- `sd serve` stays offline: browser libraries are vendored into `internal/web/static/vendor/` from `web/package.json`
  (`cd web && npm ci && npm run vendor`), never loaded from a CDN. The Go build never needs npm.
- Schemas (`module.json`, `quiz.json`, `progress.json`) are documented in MODULE_TEMPLATE.md §5, §8 and §8b. Change
  the docs and the Go types together, and bump `schema_version` only for breaking changes.
- Policy thresholds belong in `course/course.json`, not in code.
- Keep the `go 1.22` line in `go.mod` unless you also update B0 Unit 2 §6: its `time.After` timer-leak lesson
  depends on the pre-1.23 timer semantics that this line selects.
- Run `gofmt`, `go vet ./...` and `go test -race ./cmd/... ./internal/...`, then `./check.sh`.

## Repo facts

- `labs/` holds stubs with failing tests (the learner's work), and `solutions/` holds reference solutions with
  byte-identical tests. `go test ./...` fails by design, because the labs fail. Use `./check.sh`.
- `progress.json` is learner state, and it's committed so it survives cloud sessions.
- Commit messages are imperative and describe the change. Don't put model names or identifiers in commits or files.

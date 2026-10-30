#!/usr/bin/env bash
# check.sh — verifies the course repo is healthy:
#   * gofmt / go vet clean (also with the breakit build tag)
#   * `sd validate` passes (metadata, quizzes, lesson markup, lab/solution pairs)
#   * CLI/internal tests pass with -race
#   * every reference solution passes its tests with -race
#   * every lab stub FAILS its tests (quickly, without panicking or timing out)
#   * lab and solution *_test.go files are byte-identical
# Run it before every commit that touches labs/, solutions/, modules/ or the tool.
set -uo pipefail
cd "$(dirname "$0")"

red() { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
step() { printf '\n== %s\n' "$*"; }
failures=0
fail() { red "FAIL: $*"; failures=$((failures + 1)); }

RACE="-race"
if [ "$(go env CGO_ENABLED)" != "1" ]; then
  red "cgo disabled: running without -race (install a C toolchain to enable it)"
  RACE=""
fi

step "go version"
go version || { red "Go is not installed"; exit 1; }

step "gofmt"
unformatted=$(gofmt -l cmd internal labs solutions 2>/dev/null)
if [ -n "$unformatted" ]; then fail "gofmt needed on:"; echo "$unformatted"; else green "ok"; fi

step "go vet ./..."
if go vet ./...; then green "ok"; else fail "go vet"; fi

step "go vet -tags breakit ./labs/... ./solutions/..."
if go vet -tags breakit ./labs/... ./solutions/...; then green "ok"; else fail "go vet -tags breakit"; fi

step "sd validate"
if go run ./cmd/sd validate; then green "ok"; else fail "sd validate"; fi

step "tool tests (cmd, internal) $RACE"
if go test $RACE -count=1 ./cmd/... ./internal/...; then green "ok"; else fail "tool tests"; fi

step "reference solutions pass $RACE"
if [ -d solutions ] && [ -n "$(go list ./solutions/... 2>/dev/null)" ]; then
  if go test $RACE -count=1 ./solutions/...; then green "ok"; else fail "solutions"; fi
else
  echo "(no solutions yet)"
fi

step "lab stubs fail (each package must fail, fast, without panic)"
for pkg in $(go list ./labs/... 2>/dev/null); do
  dir=$(go list -f '{{.Dir}}' "$pkg")
  if ! ls "$dir"/*_test.go >/dev/null 2>&1; then continue; fi
  out=$(go test -count=1 -timeout 120s "$pkg" 2>&1)
  status=$?
  rel=${dir#"$PWD"/}
  if [ $status -eq 0 ]; then
    fail "$rel passes on the stub — tests are too weak or the stub is implemented"
  elif echo "$out" | grep -q "panic: test timed out"; then
    fail "$rel timed out on the stub — stubs must return immediately and tests must use timeouts"
  elif echo "$out" | grep -q "^panic: "; then
    fail "$rel panicked on the stub — stubs must return zero values / errNotImplemented, not panic"
  else
    echo "  fails as expected: $rel"
  fi
done

step "lab and solution test files are identical"
before=$failures
for labdir in labs/*/*/; do
  [ -d "$labdir" ] || continue
  soldir="solutions/${labdir#labs/}"
  for f in "$labdir"*_test.go; do
    [ -e "$f" ] || continue
    # breakit-only packages may intentionally have no solution counterpart
    if [ ! -d "$soldir" ] && grep -q '^//go:build breakit' "$f"; then continue; fi
    sf="$soldir$(basename "$f")"
    if [ ! -e "$sf" ]; then fail "missing $sf"; continue; fi
    cmp -s "$f" "$sf" || fail "$f differs from $sf"
  done
  if [ -d "$soldir" ]; then
    for sf in "$soldir"*_test.go; do
      [ -e "$sf" ] || continue
      [ -e "$labdir$(basename "$sf")" ] || fail "solution test $sf has no lab counterpart"
    done
  fi
done
[ $failures -eq "$before" ] && green "ok"

echo
if [ $failures -eq 0 ]; then
  green "check.sh: all checks passed"
else
  red "check.sh: $failures check(s) failed"
  exit 1
fi

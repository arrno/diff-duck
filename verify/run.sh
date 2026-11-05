#!/usr/bin/env bash
# Full verification from one command: Go tests and the CLI suite, then the viewer in a real
# browser, opened from output the real CLI produced. Exits nonzero if anything fails.
set -euo pipefail
cd "$(dirname "$0")/.."
# -count=1 because the CLI suite builds the binary itself, which the test cache cannot see.
go test -count=1 ./...

export VERIFY_WORK; VERIFY_WORK="$(mktemp -d)"
trap 'rm -rf "$VERIFY_WORK"' EXIT
fixture="$VERIFY_WORK/fixture"
verify/make-fixture.sh "$fixture"
go build -tags verify -o "$VERIFY_WORK/duck-diff" ./cmd/duck-diff
bin="$VERIFY_WORK/duck-diff"
"$bin" --adapter scripted:normal --no-cache --out "$VERIFY_WORK/normal" "$fixture" base head >/dev/null
"$bin" --structure-only --out "$VERIFY_WORK/empty" "$fixture" base base >/dev/null
set +e
"$bin" --adapter scripted:fail --no-cache --out "$VERIFY_WORK/failed" "$fixture" base head >/dev/null 2>&1
code=$?
set -e
[ "$code" -eq 3 ] || { echo "expected exit 3 from a total model failure, got $code"; exit 1; }

npx playwright install chromium >/dev/null
npx playwright test --config verify/playwright.config.ts
echo "VERIFIED"

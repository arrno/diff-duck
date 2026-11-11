#!/usr/bin/env bash
# Full verification from one command: Go tests and the CLI suite, then the viewer in a real
# browser, opened from output the real CLI produced. Exits nonzero if anything fails.
set -euo pipefail
cd "$(dirname "$0")/.."
# -count=1 because the CLI suite builds the binary itself, which the test cache cannot see.
go test -count=1 ./...

export VERIFY_WORK; VERIFY_WORK="$(mktemp -d)"
server=""
trap '[ -n "$server" ] && kill "$server" 2>/dev/null; rm -rf "$VERIFY_WORK"' EXIT
fixture="$VERIFY_WORK/fixture"
verify/make-fixture.sh "$fixture"
go build -tags verify -o "$VERIFY_WORK/diffduck" ./cmd/diffduck
bin="$VERIFY_WORK/diffduck"
"$bin" --adapter scripted:normal --no-cache --out "$VERIFY_WORK/normal" "$fixture" base head >/dev/null
"$bin" --structure-only --out "$VERIFY_WORK/empty" "$fixture" base base >/dev/null
verify/make-paths-fixture.sh "$VERIFY_WORK/paths-fixture"
"$bin" --structure-only --out "$VERIFY_WORK/paths" "$VERIFY_WORK/paths-fixture" base head >/dev/null
set +e
"$bin" --adapter scripted:fail --no-cache --out "$VERIFY_WORK/failed" "$fixture" base head >/dev/null 2>&1
code=$?
set -e
[ "$code" -eq 3 ] || { echo "expected exit 3 from a total model failure, got $code"; exit 1; }

# The local app, on a free port, with its own database. Stand-in claude and codex binaries make
# both CLI providers available whatever this machine has installed; the scripted model means
# neither is ever run.
stubs="$VERIFY_WORK/stub-bin"
mkdir -p "$stubs"
for name in claude codex; do printf '#!/bin/sh\nexit 1\n' >"$stubs/$name"; chmod +x "$stubs/$name"; done
PATH="$stubs:$PATH" DIFFDUCK_HOME="$VERIFY_WORK/home" "$bin" serve --adapter scripted:normal --no-cache --no-open --port 0 --db "$VERIFY_WORK/app/diffs.db" >"$VERIFY_WORK/server.log" 2>&1 &
server=$!
for _ in $(seq 1 100); do
  APP_URL="$(grep -o 'http://127.0.0.1:[0-9]*/' "$VERIFY_WORK/server.log" || true)"
  [ -n "$APP_URL" ] && break
  kill -0 "$server" || { cat "$VERIFY_WORK/server.log"; exit 1; }
  perl -e 'select(undef, undef, undef, 0.1)'
done
[ -n "$APP_URL" ] || { echo "the app did not start"; exit 1; }
export APP_URL APP_FIXTURE="$fixture"

npx playwright install chromium >/dev/null
npx playwright test --config verify/playwright.config.ts
echo "VERIFIED"

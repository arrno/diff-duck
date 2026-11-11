#!/usr/bin/env bash
# Builds a repository whose change spans nested directories and a very long file path, to check
# the order of packages and how long names lay out.
set -euo pipefail
dir="$1"
long="docs/architecture/decisions/2026/accepted/a-very-long-directory-name-for-layout/a-very-long-file-name-that-keeps-going.md"
rm -rf "$dir"; mkdir -p "$dir"
cd "$dir"
git init -q -b main
git config user.name fixture; git config user.email fixture@example.com
export GIT_AUTHOR_DATE="2026-01-01T00:00:00Z" GIT_COMMITTER_DATE="2026-01-01T00:00:00Z"
for d in apps/portal/src apps/portal/src/components/session-clock apps/portal/src-utils; do mkdir -p "$d"; echo 'export function a() { return 1; }' > "$d/index.ts"; done
mkdir -p "$(dirname "$long")"; echo "old" > "$long"
git add -A; git commit -q -m base; git tag base
for d in apps/portal/src apps/portal/src/components/session-clock apps/portal/src-utils; do echo 'export function a() { return 2; }' > "$d/index.ts"; done
echo "new" > "$long"
git add -A; git commit -q -m head; git tag head

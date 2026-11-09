#!/usr/bin/env bash
# Builds the embedded tree-sitter WASM modules: one module per language, each holding the
# tree-sitter core, that language's grammar, and the shared shim.
#
#   scripts/build-treesitter-wasm.sh python      # one language
#   scripts/build-treesitter-wasm.sh all         # every scripts/treesitter/*.env
#   scripts/build-treesitter-wasm.sh --docker all  # same, inside a Linux container
#
# Inputs are pinned: the tree-sitter core and every grammar come from the Go module proxy at a
# fixed version (verified by sum.golang.org), and the compiler is zig $ZIG_VERSION. The output is
# byte-for-byte reproducible from those inputs. Output goes to internal/adapters/treesitter/wasm,
# together with the licence texts of everything compiled into the modules.
set -euo pipefail

ZIG_VERSION=0.13.0
CORE_MODULE=github.com/tree-sitter/go-tree-sitter
CORE_VERSION=v0.25.0
# Docker fallback: a pinned Go image plus the official zig release tarball, checked by sha256.
DOCKER_IMAGE=golang:1.26
ZIG_SHA256_x86_64=d45312e61ebcc48032b77bc4cf7fd6915c11fa16e4aad116b66c9468211230ea
ZIG_SHA256_aarch64=041ac42323837eb5624068acd8b00cd5777dac4cf91179e8dad7a7e90dd0c556

root="$(cd "$(dirname "$0")/.." && pwd)"
manifests="$root/scripts/treesitter"
out="$root/internal/adapters/treesitter/wasm"
shim="$root/internal/adapters/treesitter/shim/shim.c"

usage() {
  echo "usage: $0 [--docker] <language|all>" >&2
  echo "languages: $(cd "$manifests" && ls *.env | sed 's/\.env$//' | tr '\n' ' ')" >&2
  exit 2
}

if [ "${1:-}" = "--docker" ]; then
  shift
  [ $# -eq 1 ] || usage
  exec docker run --rm -v "$root":/src -w /src "$DOCKER_IMAGE" bash -euc '
    arch="$(uname -m)"
    case "$arch" in x86_64) sum='"$ZIG_SHA256_x86_64"' ;; aarch64|arm64) arch=aarch64; sum='"$ZIG_SHA256_aarch64"' ;; *) echo "unsupported $arch"; exit 1 ;; esac
    apt-get update -qq >/dev/null && apt-get install -y -qq xz-utils >/dev/null
    curl -sSfL -o /tmp/zig.tar.xz "https://ziglang.org/download/'"$ZIG_VERSION"'/zig-linux-$arch-'"$ZIG_VERSION"'.tar.xz"
    echo "$sum  /tmp/zig.tar.xz" | sha256sum -c - >/dev/null
    tar -xJf /tmp/zig.tar.xz -C /opt
    export ZIG="/opt/zig-linux-$arch-'"$ZIG_VERSION"'/zig"
    scripts/build-treesitter-wasm.sh "$0"
  ' "$1"
fi

[ $# -eq 1 ] || usage
zig="${ZIG:-zig}"
have="$("$zig" version 2>/dev/null || true)"
if [ "$have" != "$ZIG_VERSION" ]; then
  echo "zig $ZIG_VERSION is required (found '${have:-none}'). Install it, set ZIG=/path/to/zig, or use --docker." >&2
  exit 1
fi

# manifest_value FILE KEY prints KEY's value. Manifests are data, never sourced.
manifest_value() {
  local v
  v="$(grep -E "^$2=" "$1" | head -n1 | cut -d= -f2-)"
  [ -n "$v" ] || { echo "$1: missing $2" >&2; exit 1; }
  printf '%s' "$v"
}

# module_dir MODULE VERSION downloads a module through the proxy and prints its directory.
module_dir() {
  local dir
  dir="$(cd "$work" && GOFLAGS=-mod=mod go mod download -json "$1@$2" | sed -n 's/^[[:space:]]*"Dir": "\(.*\)",$/\1/p')"
  [ -d "$dir" ] || { echo "could not download $1@$2" >&2; exit 1; }
  printf '%s' "$dir"
}

work="$(mktemp -d)"
trap 'chmod -R u+w "$work" 2>/dev/null; rm -rf "$work"' EXIT
core="$(module_dir "$CORE_MODULE" "$CORE_VERSION")"
zig_lib="$("$zig" env | sed -n 's/^[[:space:]]*"lib_dir": "\(.*\)",$/\1/p')"
mkdir -p "$out/LICENSES"

# Licences of what every module contains: the core, and the wasi-libc/musl code zig links in.
cat "$core/LICENSE" > "$out/LICENSES/tree-sitter.txt"
cat "$zig_lib/libc/wasi/LICENSE" "$zig_lib/libc/wasi/LICENSE-MIT" "$zig_lib/libc/musl/COPYRIGHT" \
  > "$out/LICENSES/wasi-libc.txt"

build() {
  local lang="$1" manifest="$manifests/$1.env"
  [ -f "$manifest" ] || { echo "no manifest $manifest" >&2; usage; }
  local module version srcdir symbol grammar
  module="$(manifest_value "$manifest" GRAMMAR_MODULE)"
  version="$(manifest_value "$manifest" GRAMMAR_VERSION)"
  srcdir="$(manifest_value "$manifest" GRAMMAR_SRC)"
  symbol="$(manifest_value "$manifest" GRAMMAR_SYMBOL)"
  [[ "$symbol" =~ ^tree_sitter_[a-z_]+$ ]] || { echo "$manifest: bad GRAMMAR_SYMBOL" >&2; exit 1; }
  [[ "$srcdir" =~ ^[a-z_]+(/[a-z_]+)*$ ]] || { echo "$manifest: bad GRAMMAR_SRC" >&2; exit 1; }
  grammar="$(module_dir "$module" "$version")"
  local sources=("$grammar/$srcdir/parser.c")
  [ -f "$grammar/$srcdir/scanner.c" ] && sources+=("$grammar/$srcdir/scanner.c")

  # Copies keep compiler inputs at stable relative paths, so the output does not depend on
  # where the module cache lives.
  local b="$work/$lang"
  mkdir -p "$b"
  cp -R "$core/src" "$b/core-src"
  cp -R "$core/include" "$b/core-include"
  # GRAMMAR_SHARED (optional) lists module directories the sources include by relative path,
  # such as tree-sitter-typescript's ../../common/scanner.h. The sources then keep their place
  # in the module's layout (zig resolves "../" lexically, so a symlink would not do).
  local shared gdir=grammar
  shared="$(grep -E '^GRAMMAR_SHARED=' "$manifest" | head -n1 | cut -d= -f2- || true)"
  if [ -n "$shared" ]; then
    [[ "$shared" =~ ^[a-z_]+( [a-z_]+)*$ ]] || { echo "$manifest: bad GRAMMAR_SHARED" >&2; exit 1; }
    mkdir -p "$b/module/$srcdir"
    cp -R "$grammar/$srcdir/." "$b/module/$srcdir/"
    for d in $shared; do cp -R "$grammar/$d" "$b/module/$d"; done
    gdir="module/$srcdir"
  else
    mkdir -p "$b/grammar"
    cp -R "$grammar/$srcdir/." "$b/grammar/"
  fi
  cp "$shim" "$b/shim.c"
  chmod -R u+w "$b" # module cache files are read-only
  local rel=("$gdir/parser.c")
  [ -f "$b/$gdir/scanner.c" ] && rel+=("$gdir/scanner.c")

  (cd "$b" && "$zig" cc --target=wasm32-wasi-musl -mexec-model=reactor -Os -std=c11 \
    -DHAVE_ENDIAN_H -D_POSIX_C_SOURCE=200112L -D_DEFAULT_SOURCE -DNDEBUG "-DTS_LANGUAGE=$symbol" \
    -Icore-include -Icore-src "-I$gdir" \
    shim.c core-src/lib.c "${rel[@]}" -o module.wasm \
    -Wl,--no-entry -Wl,--strip-all -Wl,-z,stack-size=1048576 \
    -Wl,--export=dd_parse -Wl,--export=dd_result -Wl,--export=dd_status \
    -Wl,--export=dd_symbol_count -Wl,--export=dd_symbol_name \
    -Wl,--export=dd_field_count -Wl,--export=dd_field_name \
    -Wl,--export=dd_alloc -Wl,--export=dd_free)
  cp "$b/module.wasm" "$out/$lang.wasm"
  chmod 0644 "$out/$lang.wasm"
  {
    echo "$module $version (grammar compiled into $lang.wasm)"
    echo
    cat "$grammar/LICENSE"
  } > "$out/LICENSES/$lang.txt"
  echo "$lang: $(wc -c < "$out/$lang.wasm" | tr -d ' ') bytes, sha256 $(shasum -a 256 "$out/$lang.wasm" 2>/dev/null | cut -d' ' -f1 || sha256sum "$out/$lang.wasm" | cut -d' ' -f1)"
}

if [ "$1" = "all" ]; then
  for m in "$manifests"/*.env; do
    build "$(basename "$m" .env)"
  done
else
  [[ "$1" =~ ^[a-z_]+$ ]] || usage
  build "$1"
fi

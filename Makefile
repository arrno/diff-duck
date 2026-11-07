.PHONY: build viewer viewer-dev test verify treesitter-wasm
build:
	go build -o bin/diffduck ./cmd/diffduck
# The built page is committed so `go build` needs no Node toolchain.
viewer:
	cd clients/viewer && npm ci && npm run build
	cp clients/viewer/dist/index.html internal/controller/htmlview/viewer.html
# Live-reloads the UI. Against a saved run: make viewer-dev DIFF=path/to/compressed-diff.json
# Or against the app, with `diffduck serve --no-open` running: make viewer-dev
viewer-dev:
	cd clients/viewer && DIFF_DUCK_JSON="$(if $(DIFF),$(abspath $(DIFF)))" npm run dev
test:
	go test -count=1 ./...
verify: build
	verify/run.sh
# Rebuilds the embedded tree-sitter grammars (committed, so `go build` needs no C toolchain).
# Needs zig 0.13.0, or Docker with TREESITTER_FLAGS=--docker. One language: GRAMMAR=python
treesitter-wasm:
	scripts/build-treesitter-wasm.sh $(TREESITTER_FLAGS) $(or $(GRAMMAR),all)

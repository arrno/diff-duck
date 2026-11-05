.PHONY: build viewer viewer-dev test verify
build:
	go build -o bin/duck-diff ./cmd/duck-diff
# The built page is committed so `go build` needs no Node toolchain.
viewer:
	cd clients/viewer && npm ci && npm run build
	cp clients/viewer/dist/index.html internal/controller/htmlview/viewer.html
# Live-reloads the viewer against a saved run: make viewer-dev DIFF=path/to/compressed-diff.json
viewer-dev:
	cd clients/viewer && DUCK_DIFF_JSON="$(abspath $(DIFF))" npm run dev
test:
	go test -count=1 ./...
verify: build
	verify/run.sh

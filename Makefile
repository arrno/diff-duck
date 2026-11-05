.PHONY: build viewer test verify
build:
	go build -o bin/duck-diff ./cmd/duck-diff
# The built page is committed so `go build` needs no Node toolchain.
viewer:
	cd clients/viewer && npm ci && npm run build
	cp clients/viewer/dist/index.html internal/controller/htmlview/viewer.html
test:
	go test -count=1 ./...
verify: build
	verify/run.sh

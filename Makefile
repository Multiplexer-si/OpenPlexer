.PHONY: check build fmt release

unexport VERSION

check:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed on:"; gofmt -l .; exit 1; }
	go vet ./...
	go test -count=1 ./...

build:
	go build -o bin/ ./apps/...

release: export RELEASE_VERSION := $(value VERSION)
release:
	go run ./tools/release -version "$$RELEASE_VERSION"

fmt:
	gofmt -w .

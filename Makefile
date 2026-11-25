.PHONY: check build fmt release

unexport VERSION

go_files = find . -type d -name node_modules -prune -o -type f -name '*.go' ! -name '.*' -print0

check:
	@test -z "$$($(go_files) | xargs -0 gofmt -l)" || { echo "gofmt needed on:"; $(go_files) | xargs -0 gofmt -l; exit 1; }
	go vet ./...
	go test -count=1 ./...

build:
	go build -o bin/ ./apps/...

release: export RELEASE_VERSION := $(value VERSION)
release:
	go run ./tools/release -version "$$RELEASE_VERSION"

fmt:
	$(go_files) | xargs -0 gofmt -w

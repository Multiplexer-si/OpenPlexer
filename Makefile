.PHONY: check build fmt

check:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed on:"; gofmt -l .; exit 1; }
	go vet ./...
	go test ./...

build:
	go build -o bin/ ./apps/...

fmt:
	gofmt -w .

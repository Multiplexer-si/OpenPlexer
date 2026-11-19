.PHONY: check build fmt

check:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed on:"; gofmt -l .; exit 1; }
	go vet ./...
	go test -count=1 ./...

build:
	go build -o bin/ ./apps/...

fmt:
	gofmt -w .

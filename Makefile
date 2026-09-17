.PHONY: build test vet run check

build:
	go build -trimpath -o bin/taillow ./cmd/taillow

test:
	go test -race ./...

vet:
	go vet ./...

# Needs TS_AUTHKEY in your environment (a tagged, ephemeral, pre-authorized key).
run:
	go run ./cmd/taillow -state-dir .tsnet-state

# What a change has to pass: formatted, vetted, tested under the race detector.
check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	go vet ./...
	go test -race ./...

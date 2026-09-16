.PHONY: build test vet run

build:
	go build -trimpath -o bin/taillow ./cmd/taillow

test:
	go test ./...

vet:
	go vet ./...

# Needs TS_AUTHKEY in your environment (a tagged, ephemeral, pre-authorized key).
run:
	go run ./cmd/taillow -state-dir .tsnet-state

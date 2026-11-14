.PHONY: build test

build:
	go build -trimpath -o autoclaude ./cmd/autoclaude

test:
	go vet ./...
	go test -race ./...

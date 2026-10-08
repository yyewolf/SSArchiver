GOLANGCI := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
LDFLAGS  := -s -w

.PHONY: generate build test lint tidy

generate: gen-go

gen-go:
	go generate ./internal/db

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ssarchiver ./cmd/ssarchiver

test:
	go test -race ./...

lint:
	$(GOLANGCI) run

tidy:
	go mod tidy

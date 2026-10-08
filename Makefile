GOLANGCI := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
LDFLAGS  := -s -w

SHADCN := go run github.com/axadrn/shadcn-templ/v2/cmd/shadcn-templ@v2.0.0-beta.13

.PHONY: generate build test lint tidy viewer gen-go gen-templ gen-css

generate: gen-go gen-templ gen-css

gen-go:
	go generate ./internal/db

gen-templ:
	go tool templ generate

gen-css: bin/tailwindcss
	./bin/tailwindcss -i internal/web/assets/css/globals.css -o internal/web/static/css/app.css --minify

bin/tailwindcss:
	./scripts/fetch-tailwind.sh

viewer:
	go generate ./internal/viewer

build: viewer
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ssarchiver ./cmd/ssarchiver

test:
	go test -race ./...

lint:
	$(GOLANGCI) run

tidy:
	go mod tidy

.PHONY: run dev
run: build
	./bin/ssarchiver serve --data-dir ./data --log-level debug

dev:
	go tool templ generate --watch --proxy="http://localhost:8080" --cmd="go run ./cmd/ssarchiver serve --data-dir ./data --log-level debug"

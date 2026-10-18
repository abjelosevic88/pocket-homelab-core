VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE   ?= ghcr.io/abjelosevic88/pocket-homelab-core
LDFLAGS  = -s -w -X main.version=$(VERSION)

.PHONY: all web build run dev test lint docker docker-multiarch backup nix clean help

all: web build

web:                       ## build the React UI into web/dist
	cd web && npm ci --no-audit --no-fund && npm run build

build:                     ## build the Go binary (embeds web/dist)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o pocket-core ./cmd/pocket-core

run: build                 ## run locally with demo data and no login
	DATA_DIR=./data DEMO_DATA=true AUTH_MODE=none LOG_LEVEL=debug ./pocket-core

dev:                       ## backend on :8080 + vite dev server on :5173 with hot reload
	@echo "Starting API on :8080 and Vite on :5173 (open http://localhost:5173)"
	@(DATA_DIR=./data DEMO_DATA=true AUTH_MODE=none LOG_LEVEL=debug go run ./cmd/pocket-core &) && cd web && npm run dev

test:                      ## run Go tests and type-check the UI
	go vet ./... && go test ./...
	cd web && npx tsc --noEmit

lint:
	gofmt -l . && go vet ./...

docker:                    ## build the container image for the local platform
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

docker-multiarch:          ## build & push amd64 + arm64 (needs buildx)
	docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) -t $(IMAGE):latest --push .

backup:                    ## snapshot the local database
	./pocket-core backup

nix:                       ## build with Nix (deploy/nix/flake.nix)
	nix build ./deploy/nix#pocket-core

clean:
	rm -rf pocket-core web/dist web/node_modules dist result

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

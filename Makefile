# Building and running the delivery-analytics app.
#
# The one rule worth knowing: `run` depends on the built client, and the built
# client depends on its sources. The server serves web/dist, which is
# gitignored, so a `git pull` updates the source and leaves the bundle the
# browser actually receives untouched — which looks exactly like a change that
# did not work. Making the dependency explicit is what stops that happening.

GO      ?= go
NPM     ?= npm
BIN     := bin/server
WEB_DIST := web/dist/index.html

# Everything the client is built from. Listed so a rebuild happens when a
# source changes and not otherwise.
WEB_SOURCES := $(shell find web/src -type f 2>/dev/null) \
               web/index.html web/package.json web/vite.config.ts web/tsconfig.json

.DEFAULT_GOAL := help

## help: list the targets
help:
	@echo "Targets:"
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | \
		awk '{ i = index($$0, ":"); printf "  %-8s %s\n", substr($$0, 1, i - 1), substr($$0, i + 2) }'

## run: build the client if needed, then serve the API and the client on :8080
run: $(WEB_DIST)
	$(GO) run ./cmd/server

## dev: same, but with the client on Vite with hot reload
dev: web/node_modules
	@trap 'kill 0' EXIT INT TERM; \
	(cd web && $(NPM) run dev) & \
	$(GO) run ./cmd/server

## build: compile the server binary and the client
build: $(BIN) $(WEB_DIST)

$(BIN): $(shell find cmd internal -name '*.go' 2>/dev/null) go.mod go.sum
	$(GO) build -o $(BIN) ./cmd/server

## web: build the client into web/dist
web: $(WEB_DIST)

$(WEB_DIST): web/node_modules $(WEB_SOURCES)
	cd web && $(NPM) run build

web/node_modules: web/package.json web/package-lock.json
	cd web && $(NPM) install
	@touch $@

## test: run the Go tests under the race detector and typecheck the client
test: web/node_modules
	$(GO) test -race ./...
	$(GO) vet ./...
	@test -z "$$(gofmt -l ./cmd ./internal)" || { echo "gofmt:"; gofmt -l ./cmd ./internal; exit 1; }
	cd web && $(NPM) run typecheck

## probe: check what the live Jira and GitHub APIs support — make probe ARGS="-sprint 7355 -repo owner/name"
probe:
	$(GO) run ./cmd/probe $(ARGS)

## fmt: format the Go sources
fmt:
	gofmt -w ./cmd ./internal

## clean: remove build output
clean:
	rm -rf $(BIN) web/dist web/tsconfig.tsbuildinfo

.PHONY: help run dev build web test probe fmt clean

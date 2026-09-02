BINARY  ?= socrates
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build run test race fmt fmt-check vet tidy-check check e2e clean docker install

## build: compile the single binary into ./socrates
build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .

## run: build and start the server on http://localhost:8080
run: build
	./$(BINARY)

## install: install the binary into $GOPATH/bin
install:
	go install -trimpath -ldflags "$(LDFLAGS)" .

## test: run the test suite
test:
	go test ./...

## race: the test suite under the race detector, which is what CI runs
race:
	go test -race ./...

## fmt: reformat the tree - the only target here that rewrites your files
fmt:
	gofmt -l -w .

# fmt-check names the files gofmt would change and fails. CI does exactly this,
# so `make check` has to as well: a check that silently rewrites the tree is not
# a check, and it hides from you what CI is about to fail on.
fmt-check:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
	  echo "These files need gofmt:"; \
	  echo "$$unformatted"; \
	  echo "run: make fmt"; \
	  exit 1; \
	fi

vet:
	go vet ./...

# tidy-check fails when go.mod or go.sum would change - an unused dependency
# that crept back in, or a missing one.
tidy-check:
	go mod tidy -diff

## check: everything CI runs, and it changes nothing
check: fmt-check vet tidy-check race build

## e2e: the browser suite - a real server, the fake CLIs on PATH, real tmux
## sessions and Playwright driving the page. It is deliberately not part of
## `check` and not part of CI: it wants node, a Chromium and tmux >= 3.3, and
## it starts real terminal sessions that outlive the server, which is exactly
## what it is there to test.
e2e: build
	@if [ ! -f e2e/run.mjs ]; then \
	  echo "e2e/run.mjs is not in this tree - the browser suite is left out of the Docker build context."; \
	  exit 1; \
	fi
	node e2e/run.mjs

## vendor-xterm re-downloads the pinned xterm.js bundle set into
## internal/web/static/vendor. The files it writes are committed, so this is
## for an upgrade and never for a build: CI never runs it.
vendor-xterm: ## re-download the pinned xterm.js bundle set
	@bash scripts/vendor-xterm.sh

docker:
	docker build -t socrates:$(VERSION) .

clean:
	rm -f $(BINARY)

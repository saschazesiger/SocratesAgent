BINARY  ?= socrates
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test race fmt fmt-check vet tidy-check check e2e vendor-ds vendor-markdown ds-check clean

## build: compile the single binary into ./socrates, for the tests and a quick
## compile check. scripts/install.sh and scripts/update.sh build the installed
## one.
build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .

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
	  echo "e2e/run.mjs is not in this tree."; \
	  exit 1; \
	fi
	node e2e/run.mjs

## vendor-xterm re-downloads the pinned xterm.js bundle set into
## internal/web/static/vendor. The files it writes are committed, so this is
## for an upgrade and never for a build: CI never runs it.
vendor-xterm: ## re-download the pinned xterm.js bundle set
	@bash scripts/vendor-xterm.sh

## vendor-markdown re-downloads the pinned Markdown renderer and sanitizer the
## chat view uses into internal/web/static/vendor/markdown. Committed and
## checksummed like the terminal; CI never runs it.
vendor-markdown: ## re-download the pinned marked and DOMPurify
	@bash scripts/vendor-markdown.sh

## vendor-ds replaces the vendored Dashboard-Style with the latest main of
## github.com/saschazesiger/Dashboard-Style, through that commit's own
## vendor.sh. DS_REF picks a branch, tag or commit instead
## (make vendor-ds DS_REF=<sha>): vendoring by commit is how a release that
## is not on main yet comes in. The copy is committed and checksummed; the
## nightly update-dashboard-style workflow does the same on its own.
DS_DEST := internal/web/static/vendor/dashboard-style
DS_REPO := saschazesiger/Dashboard-Style
DS_REF  ?= main
vendor-ds: ## update the vendored Dashboard-Style to its latest main (or DS_REF)
	@tmp=$$(mktemp -d) && trap 'rm -rf "$$tmp"' EXIT && \
	curl -fsSL "https://raw.githubusercontent.com/$(DS_REPO)/$(DS_REF)/scripts/vendor.sh" -o "$$tmp/vendor.sh" && \
	sh "$$tmp/vendor.sh" "$(DS_REF)" "$(DS_DEST)"

## ds-check verifies the vendored copy against its SHA256SUMS (the same check
## internal/web's tests make, so `make check` covers it) and, where node is
## installed, runs Dashboard-Style's project check with ds-check.json (CI and
## the nightly update run it too).
ds-check:
	go test ./internal/web/ -run 'TestVendoredDashboardStyleIsUntouched' -count=1
	@if command -v node >/dev/null 2>&1; then \
	  node $(DS_DEST)/tools/check-project.mjs --config ds-check.json; \
	else \
	  echo "node is not installed: the project check is left to CI"; \
	fi

clean:
	rm -f $(BINARY)

BINARY  ?= socrates
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build run test fmt vet check clean docker install

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

fmt:
	gofmt -l -w .

vet:
	go vet ./...

## check: everything CI runs
check: fmt vet test build

docker:
	docker build -t socrates:$(VERSION) .

clean:
	rm -f $(BINARY)
	rm -rf dist

# tmux-qs Makefile
# Common dev workflow targets. The README is the source of truth for
# end-user install instructions; this file is for contributors and CI.

BINARY     ?= tmux-qs
VERSION    ?= dev
LDFLAGS    ?= -s -w -X main.version=$(VERSION)
INSTALL    ?= $(HOME)/bin
GO         ?= go
GOFLAGS    ?=

.PHONY: all build install test test-verbose test-race test-coverage lint vet fmt fmt-check clean help

all: build

## help: Print this help message
help:
	@echo "tmux-qs build targets:"
	@echo "  make build            - compile the binary"
	@echo "  make install          - build + copy binary to \$$HOME/bin"
	@echo "  make test             - run unit tests"
	@echo "  make test-verbose     - run unit tests with -v"
	@echo "  make test-race        - run unit tests with -race (needs tmux server)"
	@echo "  make test-coverage    - run tests + emit coverage report"
	@echo "  make lint             - run go vet (cheap static checks)"
	@echo "  make vet              - alias for lint"
	@echo "  make fmt              - run gofmt -w on the source"
	@echo "  make fmt-check        - check that the source is gofmt-clean (CI)"
	@echo "  make clean            - remove build artifacts"

## build: Compile the binary
build:
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) .

## install: Build and copy to $$HOME/bin
install: build
	install -m 0755 $(BINARY) $(INSTALL)/$(BINARY)
	@echo "Installed $(INSTALL)/$(BINARY)"

## test: Run the unit test suite
test:
	$(GO) test $(GOFLAGS) ./...

## test-verbose: Run tests with verbose output
test-verbose:
	$(GO) test $(GOFLAGS) -v ./...

## test-race: Run tests with the race detector
test-race:
	$(GO) test $(GOFLAGS) -race ./...

## test-coverage: Run tests and emit a coverage profile
test-coverage:
	$(GO) test $(GOFLAGS) -coverprofile=coverage.out -covermode=atomic ./...
	$(GO) tool cover -func=coverage.out | tail -1

## lint: Run go vet
lint vet:
	$(GO) vet ./...

## fmt: Run gofmt -w on all Go sources
fmt:
	$(GO) fmt ./...

## fmt-check: Verify sources are gofmt-clean
fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "Unformatted files:"; echo "$$out"; exit 1; fi

## clean: Remove build artifacts
clean:
	rm -f $(BINARY) coverage.out

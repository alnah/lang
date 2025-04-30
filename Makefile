.PHONY: all dev default install run fmt lint test cover bench race todo build release clean

# Directories / files
BIN        := bin
DIST       := dist
TARGET     := $(BIN)/lang
COVER_OUT  := coverage.out
COVER_HTML := coverage.html

# Packages / sources
PKGS       := ./...
SRC        := $(shell find . -name '*.go')

# External tools
GOLANGCI   := $(shell command -v golangci-lint 2>/dev/null)
GORELEASER := $(shell command -v goreleaser 2>/dev/null)

# -----------------------------------------------------------------------------
# Aggregate targets
# -----------------------------------------------------------------------------
default: dev

all: dev build

# Developer workflow
dev: install fmt lint cover bench race todo

# -----------------------------------------------------------------------------
# Core targets
# -----------------------------------------------------------------------------
install:
	@echo ">> downloading modules"
	@go mod download

run:
	@echo ">> running"
	@go run .

fmt:
	@echo ">> checking formatting"
	@test -z "$(shell gofmt -l $(SRC))" || (gofmt -d $(SRC); exit 1)

lint:
	@echo ">> linting"
	@if [ -z "$(GOLANGCI)" ]; then \
	  echo "warning: golangci-lint not found"; \
	else \
	  $(GOLANGCI) run; \
	fi

test:
	@echo ">> running tests"
	@go test -v $(PKGS)

cover:
	@echo ">> generating cover"
	@go test -coverprofile=$(COVER_OUT) $(PKGS)
	@go tool cover -func=$(COVER_OUT)

hcover: cover
	@echo ">> opening cover report"
	@go tool cover -html=$(COVER_OUT)
	@rm $(COVER_OUT)
	@rm $(COVER_HTML)

bench:
	@echo ">> benchmarks"
	@go test -bench=. $(PKGS)

race:
	@echo ">> race detector"
	@go test -race $(PKGS)

todo:
	@echo ">> TODO / FIXME list (Go sources only)"
	@grep -R --line-number \
	    --exclude-dir=$(BIN) \
	    --exclude-dir=$(DIST) \
	    --exclude-dir=vendor \
	    --exclude-dir=.git \
	    --include='*.go' -E 'TODO|FIXME' . || true

build:
	@echo ">> building binary -> $(TARGET)"
	@mkdir -p $(BIN)
	@go build -o $(TARGET) .

release: fmt lint test bench
	@echo ">> releasing"
	@if [ -z "$(GORELEASER)" ]; then \
	  echo "warning: goreleaser not found"; \
	else \
	  $(GORELEASER) release; \
	fi

clean:
	@echo ">> cleaning"
	@rm -rf $(BIN) $(DIST) $(COVER_OUT)

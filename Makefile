PROJECT      := translator
BIN_DIR      := bin
BIN          := $(BIN_DIR)/$(PROJECT)
PKG          := ./...

GO           ?= go
GOEXPERIMENT ?= simd
CGO_ENABLED  ?= 1
GOFLAGS      ?= -trimpath

LDFLAGS      := -s -w

export CGO_ENABLED
export GOEXPERIMENT

.PHONY: all build run test test-race vet lint bench tidy clean

all: build

$(BIN_DIR):
	@mkdir -p $@

build: | $(BIN_DIR)
	$(GO) build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o $(BIN) ./cmd/translator

run: build
	./$(BIN)

test:
	$(GO) test $(PKG)

test-race:
	$(GO) test -race $(PKG)

vet:
	$(GO) vet $(PKG)

bench:
	$(GO) test -bench=. -benchmem -run=^$$ $(PKG)

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR)

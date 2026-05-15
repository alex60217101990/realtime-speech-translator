PROJECT      := translator
BIN_DIR      := bin
BIN          := $(BIN_DIR)/$(PROJECT)
PKG          := ./...

GO           ?= go
GOEXPERIMENT ?= simd
CGO_ENABLED  ?= 1
GOFLAGS      ?= -trimpath

LDFLAGS      := -s -w

UNAME_S      := $(shell uname -s)

# --- whisper.cpp env wiring -------------------------------------------------
# The bindings declare LDFLAGS but rely on the caller to set include and
# library paths. We pass them via C_INCLUDE_PATH and LIBRARY_PATH so the
# linker/preprocessor find the static libs produced by scripts/build-deps.sh.
WHISPER_DIR  := $(abspath third_party/whisper.cpp)
WHISPER_BUILD:= $(WHISPER_DIR)/build_go
WHISPER_INC  := $(WHISPER_DIR)/include:$(WHISPER_DIR)/ggml/include
WHISPER_LIB  := $(WHISPER_BUILD)/src:$(WHISPER_BUILD)/ggml/src
ifeq ($(UNAME_S),Darwin)
WHISPER_LIB  := $(WHISPER_LIB):$(WHISPER_BUILD)/ggml/src/ggml-blas:$(WHISPER_BUILD)/ggml/src/ggml-metal
# Metal shaders are loaded from this prefix at runtime.
export GGML_METAL_PATH_RESOURCES := $(WHISPER_DIR)
endif

export CGO_ENABLED
export GOEXPERIMENT
export C_INCLUDE_PATH := $(WHISPER_INC):$(C_INCLUDE_PATH)
export LIBRARY_PATH   := $(WHISPER_LIB):$(LIBRARY_PATH)

.PHONY: all build run test test-race vet lint bench tidy clean deps deps-whisper

all: build

$(BIN_DIR):
	@mkdir -p $@

# --- native deps ------------------------------------------------------------
deps: deps-whisper

deps-whisper:
	./scripts/build-deps.sh whisper

# --- Go ---------------------------------------------------------------------
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

PROJECT      := translator
BIN_DIR      := bin
BIN          := $(BIN_DIR)/$(PROJECT)
PKG          := ./...

GO           ?= go
CGO_ENABLED  ?= 1
GOFLAGS      ?= -trimpath
VERSION      ?= dev

# -ldflags injects the version string into cmd/translator/main.go and
# strips DWARF / symbol tables for a tighter binary.
LDFLAGS      := -s -w -X main.version=$(VERSION)

UNAME_S      := $(shell uname -s)

# ----------------------------------------------------------------------------
# Optional MT cgo wiring
# ----------------------------------------------------------------------------
# internal/mt is gated by the `mt` build tag because SentencePiece and
# CTranslate2 must be compiled out of third_party/ first. The default
# target builds without translation (mt.Disabled passthrough); the
# `mt` target wires in the cgo paths and adds -tags mt.
SP_DIR    := $(abspath third_party/sentencepiece)
SP_BUILD  := $(SP_DIR)/build_go
SP_INC    := $(SP_DIR)/src:$(SP_BUILD)/src
SP_LIB    := $(SP_BUILD)/src

CT2_DIR   := $(abspath third_party/ctranslate2)
CT2_BUILD := $(CT2_DIR)/build_go
CT2_INC   := $(CT2_DIR)/include
CT2_LIB   := $(CT2_BUILD):$(CT2_BUILD)/third_party/cpu_features:$(CT2_BUILD)/third_party/ruy/ruy:$(CT2_BUILD)/third_party/ruy/third_party/cpuinfo:$(CT2_BUILD)/third_party/ruy/third_party/cpuinfo/deps/clog

export CGO_ENABLED

# CGO_CXXFLAGS suppresses CT2 vendored header noise under modern clang.
export CGO_CXXFLAGS := -Wno-deprecated-literal-operator $(CGO_CXXFLAGS)

.PHONY: all build mt ui mt-ui run test test-race vet lint bench tidy clean
.PHONY: deps deps-sentencepiece deps-ctranslate2
.PHONY: package package-macos package-linux package-windows

all: build

$(BIN_DIR):
	@mkdir -p $@

# ----------------------------------------------------------------------------
# Builds
# ----------------------------------------------------------------------------

# Default: STT + TTS only (MT runs in Disabled passthrough). No
# third_party compilation required — sherpa-onnx native libs come
# from the per-platform Go module dependency.
build: | $(BIN_DIR)
	$(GO) build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o $(BIN) ./cmd/translator
	$(GO) build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o $(BIN_DIR)/probe   ./cmd/probe
	$(GO) build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o $(BIN_DIR)/stt-mic ./cmd/stt-mic
	$(GO) build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o $(BIN_DIR)/tts-say ./cmd/tts-say

# `make mt` builds the translator with SMaLL-100 / OPUS-MT enabled.
# Requires the sentencepiece + ctranslate2 submodules to be initialised
# (git submodule update --init) and compiled (make deps).
mt: | $(BIN_DIR)
	CPATH="$(SP_INC):$(CT2_INC):$$CPATH" \
	C_INCLUDE_PATH="$(SP_INC):$(CT2_INC):$$C_INCLUDE_PATH" \
	LIBRARY_PATH="$(SP_LIB):$(CT2_LIB):$$LIBRARY_PATH" \
	$(GO) build $(GOFLAGS) -tags mt -ldflags='$(LDFLAGS)' -o $(BIN) ./cmd/translator

# `make ui` builds the Ebiten translator-ui without MT (passthrough).
ui: | $(BIN_DIR)
	$(GO) build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o $(BIN_DIR)/translator-ui ./cmd/translator-ui

# `make mt-ui` builds the Ebiten translator-ui with SMaLL-100 / OPUS-MT
# wired in. Same prerequisites as `make mt`.
mt-ui: | $(BIN_DIR)
	CPATH="$(SP_INC):$(CT2_INC):$$CPATH" \
	C_INCLUDE_PATH="$(SP_INC):$(CT2_INC):$$C_INCLUDE_PATH" \
	LIBRARY_PATH="$(SP_LIB):$(CT2_LIB):$$LIBRARY_PATH" \
	$(GO) build $(GOFLAGS) -tags mt -ldflags='$(LDFLAGS)' -o $(BIN_DIR)/translator-ui ./cmd/translator-ui

run: build
	./$(BIN)

# ----------------------------------------------------------------------------
# Native dependency builds (only needed for `make mt`)
# ----------------------------------------------------------------------------
deps: deps-sentencepiece deps-ctranslate2

deps-sentencepiece:
	./scripts/build-deps.sh sentencepiece

deps-ctranslate2:
	./scripts/build-deps.sh ctranslate2

# ----------------------------------------------------------------------------
# Test, vet, bench
# ----------------------------------------------------------------------------
test:
	$(GO) test $(PKG)

test-race:
	$(GO) test -race $(PKG)

vet:
	$(GO) vet $(PKG)

fix:
	$(GO) fix $(PKG)

bench:
	$(GO) test -bench=. -benchmem -run=^$$ $(PKG)

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR) dist

# ----------------------------------------------------------------------------
# Packaging
# ----------------------------------------------------------------------------
package:
	@case "$(UNAME_S)" in \
		Darwin)  $(MAKE) package-macos ;; \
		Linux)   $(MAKE) package-linux ;; \
		MINGW*|MSYS*|CYGWIN*) $(MAKE) package-windows ;; \
		*)       echo "unsupported host: $(UNAME_S)"; exit 1 ;; \
	esac

package-macos:
	VERSION=$(VERSION) ./scripts/package-macos.sh

package-linux:
	VERSION=$(VERSION) ./scripts/package-linux.sh

package-windows:
	VERSION=$(VERSION) ./scripts/package-windows.sh

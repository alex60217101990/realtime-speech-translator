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
# The ggml-metal subdir is included in LIBRARY_PATH on Darwin regardless
# of whether Metal was actually compiled — build-deps.sh drops an empty
# libggml-metal.a stub there so the upstream cgo binding (which hardcodes
# `-lggml-metal`) still links. Apple Silicon users who opt into Metal
# with GGML_METAL=ON get a real lib in the same place.
WHISPER_DIR  := $(abspath third_party/whisper.cpp)
WHISPER_BUILD:= $(WHISPER_DIR)/build_go
WHISPER_INC  := $(WHISPER_DIR)/include:$(WHISPER_DIR)/ggml/include
WHISPER_LIB  := $(WHISPER_BUILD)/src:$(WHISPER_BUILD)/ggml/src
ifeq ($(UNAME_S),Darwin)
WHISPER_LIB  := $(WHISPER_LIB):$(WHISPER_BUILD)/ggml/src/ggml-blas:$(WHISPER_BUILD)/ggml/src/ggml-metal
# GGML_METAL_PATH_RESOURCES points the ggml-metal runtime at the
# Metal source tree so it can compile shaders on the fly. Harmless
# when Metal is off (no caller looks at the env), useful when the
# user opts back in with GGML_METAL=ON.
export GGML_METAL_PATH_RESOURCES := $(WHISPER_DIR)
endif

# --- sentencepiece env wiring -----------------------------------------------
SP_DIR    := $(abspath third_party/sentencepiece)
SP_BUILD  := $(SP_DIR)/build_go
SP_INC    := $(SP_DIR)/src:$(SP_BUILD)/src
SP_LIB    := $(SP_BUILD)/src

# --- ctranslate2 env wiring -------------------------------------------------
CT2_DIR   := $(abspath third_party/ctranslate2)
CT2_BUILD := $(CT2_DIR)/build_go
CT2_INC   := $(CT2_DIR)/include
# CT2 is itself a single static lib; ruy, cpu_features and ruy's
# bundled cpuinfo / clog are nested.
CT2_LIB   := $(CT2_BUILD):$(CT2_BUILD)/third_party/cpu_features:$(CT2_BUILD)/third_party/ruy/ruy:$(CT2_BUILD)/third_party/ruy/third_party/cpuinfo:$(CT2_BUILD)/third_party/ruy/third_party/cpuinfo/deps/clog

export CGO_ENABLED
export GOEXPERIMENT
# CPATH covers both C and C++ search paths (whereas C_INCLUDE_PATH is
# C-only). Cgo shims for SentencePiece and CTranslate2 are C++, so we
# need CPATH so #include resolves under the cgo build.
export CPATH          := $(WHISPER_INC):$(SP_INC):$(CT2_INC):$(CPATH)
export C_INCLUDE_PATH := $(WHISPER_INC):$(SP_INC):$(CT2_INC):$(C_INCLUDE_PATH)
export LIBRARY_PATH   := $(WHISPER_LIB):$(SP_LIB):$(CT2_LIB):$(LIBRARY_PATH)

# CTranslate2 vendored headers (half_float, nlohmann/json) trigger
# deprecation warnings under modern clang; silence to keep build output
# readable.
export CGO_CXXFLAGS := -Wno-deprecated-literal-operator $(CGO_CXXFLAGS)

.PHONY: all build run test test-race vet lint bench tidy clean deps deps-whisper

all: build

$(BIN_DIR):
	@mkdir -p $@

# --- native deps ------------------------------------------------------------
deps: deps-whisper deps-sentencepiece deps-ctranslate2

deps-whisper:
	./scripts/build-deps.sh whisper

deps-sentencepiece:
	./scripts/build-deps.sh sentencepiece

deps-ctranslate2:
	./scripts/build-deps.sh ctranslate2

# --- Go ---------------------------------------------------------------------
build: | $(BIN_DIR)
	$(GO) build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o $(BIN) ./cmd/translator
	$(GO) build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o $(BIN_DIR)/bench ./cmd/bench

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
	rm -rf $(BIN_DIR) dist

# --- packaging ---------------------------------------------------------------
# Per-OS installer / bundle builds. VERSION is propagated into binary
# build (-X main.version) and into the Info.plist / NSIS metadata.
VERSION ?= 0.0.0

.PHONY: package package-macos package-linux package-windows

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

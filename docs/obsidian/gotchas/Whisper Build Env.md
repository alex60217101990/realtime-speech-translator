---
title: whisper.cpp cgo needs C_INCLUDE_PATH and LIBRARY_PATH
date: 2026-05-15
tags:
  - gotcha
  - build
  - whisper
  - cgo
---

# whisper.cpp cgo needs `C_INCLUDE_PATH` and `LIBRARY_PATH`

## Symptom

```
ld: library 'whisper' not found
fatal error: 'whisper.h' file not found
```

even though `third_party/whisper.cpp/build_go/` exists and contains the libs.

## Cause

The Go bindings in `third_party/whisper.cpp/bindings/go/whisper.go` hard-code:

```go
// #cgo LDFLAGS: -lwhisper -lggml -lggml-base -lggml-cpu -lm -lstdc++
// #cgo darwin LDFLAGS: -lggml-metal -lggml-blas
// #cgo darwin LDFLAGS: -framework Accelerate -framework Metal -framework Foundation -framework CoreGraphics
// #include <whisper.h>
```

They do **not** include `-I` or `-L` paths to find these — the upstream Makefile sets them through environment instead. Reason: the binding doesn't know where you built the libs.

## Fix

Set both before `go build` or `go test`. In this repo, [[../../../Makefile|Makefile]] does:

```makefile
WHISPER_DIR  := $(abspath third_party/whisper.cpp)
WHISPER_INC  := $(WHISPER_DIR)/include:$(WHISPER_DIR)/ggml/include
WHISPER_LIB  := $(WHISPER_DIR)/build_go/src:$(WHISPER_DIR)/build_go/ggml/src:$(WHISPER_DIR)/build_go/ggml/src/ggml-blas:$(WHISPER_DIR)/build_go/ggml/src/ggml-metal
export C_INCLUDE_PATH := $(WHISPER_INC):$(C_INCLUDE_PATH)
export LIBRARY_PATH   := $(WHISPER_LIB):$(LIBRARY_PATH)
```

When running outside `make` (e.g. `go test` directly in an editor), set these in your shell or `.envrc`.

## Macos: Metal runtime resources

Add:

```bash
export GGML_METAL_PATH_RESOURCES="$REPO/third_party/whisper.cpp"
```

Required at **runtime**, not just build, so whisper.cpp can find its embedded Metal shaders. We will set this from Go via `os.Setenv` in `init()` of the wrapper so users do not need to remember.

## Prevention

Run `make build` (uses the Makefile env). Do not rely on `go build ./...` straight from the shell unless you have `direnv`/`.envrc` configured.

## Date discovered

2026-05-15 (M2 first whisper build).

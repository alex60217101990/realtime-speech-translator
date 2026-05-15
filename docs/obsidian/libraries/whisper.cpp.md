---
title: whisper.cpp
date: 2026-05-15
tags:
  - library
  - stt
  - cgo
  - submodule
aliases:
  - whisper
  - ggerganov/whisper.cpp
---

# whisper.cpp

C++ port of OpenAI's Whisper model used as the STT engine. Cgo-linked against a static library built from source.

## Vendoring

> [!important] Submodule, not Go module
> The C++ source is vendored as a **git submodule** at `third_party/whisper.cpp` (pinned to **v1.8.4** at commit `46ca43d6`). The Go bindings inside it (`bindings/go`) are imported via a `go.mod` `replace` directive:
>
> ```go
> replace github.com/ggerganov/whisper.cpp/bindings/go => ./third_party/whisper.cpp/bindings/go
> ```
>
> Updates: `git submodule update --remote third_party/whisper.cpp` then rebuild static libs.

## Build wiring

The bindings have hard-coded `#cgo LDFLAGS: -lwhisper -lggml ...` directives that expect the static libs in `LIBRARY_PATH` and headers in `C_INCLUDE_PATH`. Build them via [[../../../scripts/build-deps.sh|scripts/build-deps.sh]]:

```bash
cmake -S third_party/whisper.cpp -B third_party/whisper.cpp/build_go \
    -DCMAKE_BUILD_TYPE=Release \
    -DBUILD_SHARED_LIBS=OFF \
    -DGGML_METAL=ON          # macOS only
cmake --build third_party/whisper.cpp/build_go --target whisper -j
```

Resulting artifacts:

```
third_party/whisper.cpp/build_go/
├── src/libwhisper.a
├── ggml/src/
│   ├── libggml.a
│   ├── libggml-base.a
│   ├── libggml-cpu.a
│   ├── ggml-blas/libggml-blas.a       (macOS)
│   └── ggml-metal/libggml-metal.a     (macOS)
```

Set these env vars before `go build`:

```bash
export C_INCLUDE_PATH="$REPO/third_party/whisper.cpp/include:$REPO/third_party/whisper.cpp/ggml/include"
export LIBRARY_PATH="$REPO/third_party/whisper.cpp/build_go/src:$REPO/third_party/whisper.cpp/build_go/ggml/src:$REPO/third_party/whisper.cpp/build_go/ggml/src/ggml-blas:$REPO/third_party/whisper.cpp/build_go/ggml/src/ggml-metal"
# macOS additionally:
export CGO_LDFLAGS="-framework Foundation -framework Metal -framework MetalKit"
```

This is automated in [[../../../Makefile|Makefile]] — see [[../gotchas/Whisper Build Env]].

## Go binding layers

Two layers inside `bindings/go`:

1. **`whisper`** — thin cgo wrapper, types map 1:1 to C structs (`Context`, `Params`, `Token`). Direct access to whisper_full_*. Not for app code.
2. **`pkg/whisper`** — higher-level `Model` and `Context` interfaces. Use this in app.

```go
import "github.com/ggerganov/whisper.cpp/bindings/go/pkg/whisper"

m, err := whisper.New("models/ggml-small.bin")
defer m.Close()
ctx, _ := m.NewContext()
ctx.SetLanguage("ru")
ctx.SetThreads(4)
ctx.SetTranslate(false)        // translate=true ⇒ output English regardless of source
ctx.Process(pcm, encBeginCb, segmentCb, progressCb)
for seg, err := ctx.NextSegment(); err == nil; seg, err = ctx.NextSegment() {
    fmt.Println(seg.Start, seg.End, seg.Text)
}
```

## Model files

`.bin` (ggml) format. Quantizations: `Q5_K`, `Q8_0`, `f16`. Manifest in [[../../wiki/03-Models|wiki/03-Models]].

| Name | Size | RAM (est.) | Use case |
|---|---|---|---|
| tiny | 39 MB | 250 MB | Constrained device |
| base | 142 MB | 500 MB | Low-power desktop |
| small | 466 MB | 1.0 GB | **Default** |
| medium | 1.5 GB | 2.5 GB | High-quality |
| large-v3 | 2.9 GB | 4.2 GB | Best, slow |

Download:

```
https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-<name>.bin
```

## Built-in VAD

The binding has `SetVAD(true)` + `SetVADModelPath(...)` for whisper.cpp's own Silero-derived VAD. We use [[go-webrtcvad]] instead because:
- Custom hangover/min/max/pre-pad logic
- Real-time segmentation, not file-level pre-pass
- No extra model file to ship

## Streaming approaches

| Approach | Latency | Complexity | Used? |
|---|---|---|---|
| Batch per utterance | bounded by utterance length | Low | ✅ MVP |
| Sliding window 5 s / 1 s overlap, partials | ~1.5 s to first partial | High | Planned |
| whisper.cpp `stream` example (mic loop, no VAD) | low but jittery | Mid | No |

> [!info] Why batch first
> Partial transcripts require deduplication of overlapping windows. Implementing this correctly while VAD is also segmenting is two state machines fighting. MVP ships batched; partials come once the rest of the pipeline is stable.

## See also

- Source: `third_party/whisper.cpp/`
- Bindings doc: `bindings/go/pkg/whisper/interface.go`
- Repo: https://github.com/ggerganov/whisper.cpp
- [[../gotchas/Whisper Build Env]]
- [[../concepts/Whisper Streaming]]

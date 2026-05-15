---
title: Cgo Boundary
date: 2026-05-15
tags:
  - architecture
  - cgo
  - build
---

# Cgo Boundary

> [!info] Principle
> Cgo is allowed only at the **edges**: audio I/O, ML inference, and VAD. Everything between — buffers, channels, orchestration, UI — is pure Go. This keeps the Go race detector usable on most of the codebase and minimises the area where the runtime cannot help us.

## What lives behind cgo

| Package | Library | Cost |
|---|---|---|
| [[../libraries/malgo]] | miniaudio (C) | low; standard cross-platform audio |
| [[../libraries/go-webrtcvad]] | WebRTC VAD (C) | low |
| [[../libraries/whisper.cpp]] | whisper.cpp (C++) | high; needs static libs built via CMake |
| Future: `internal/mt/ct2` | CTranslate2 (C++) | high; same pattern as whisper |
| Future: `internal/tts/piper` | Piper (C++) or onnxruntime | medium |
| Future: `internal/audio/sample` SIMD path | Go 1.26 [[../libraries/Go 1.26 SIMD|simd/archsimd]] | none (still Go, just experimental) |

## Build implications

- **`CGO_ENABLED=1`** mandatory — see [[../gotchas/CGO_ENABLED Required]].
- Cross-compilation needs a target-matching C toolchain (zig-cc or platform-native).
- Static linking preferred where possible to keep distribution simple — see [[../../wiki/06-Build-and-Packaging]].
- Each new cgo package must come with a build-deps step in [[../../../scripts/build-deps.sh|build-deps.sh]] **and** a Makefile env entry — see [[../gotchas/Whisper Build Env]].

## Runtime implications

- Cgo callbacks (malgo Data, whisper progress) run on **C-owned OS threads**, not Go-managed M's. They require `runtime.LockOSThread` if Go-side cleanup must run on the same thread, and they must not allocate — see [[../gotchas/Audio Thread No Allocation]].
- `runtime.GOMAXPROCS` counts Go-managed M's. Whisper threads (via OpenMP / pthread spawned by C) are separate and add to CPU usage independently. Cap whisper threads via `ctx.SetThreads(runtime.NumCPU()/2)` to avoid contention with the audio thread.

## Testing

`go test -race` cannot see inside C. Concurrency bugs in cgo callbacks (writing to a Go map from two C threads, e.g.) are silent until they corrupt memory. Mitigation: callbacks only call thin wrappers that immediately push to a channel; the channel becomes the synchronisation point and the race detector covers it.

## See also

- [[../gotchas/CGO_ENABLED Required]]
- [[../gotchas/Whisper Build Env]]
- [[../gotchas/Audio Thread No Allocation]]
- [[../../wiki/06-Build-and-Packaging]]

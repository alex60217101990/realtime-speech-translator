---
title: Audio callback thread — no allocation, no blocking
date: 2026-05-15
tags:
  - gotcha
  - audio
  - performance
  - real-time
---

# Audio callback thread — no allocation, no blocking

## Why

The malgo Data callback runs on a miniaudio-owned **OS thread**, not a Go goroutine you spawned. miniaudio guarantees the call returns inside the DMA period (~10 ms for our config). If it doesn't:

- DMA buffer overruns on capture → samples lost in the driver.
- DMA buffer underruns on playback → audible glitch.
- On macOS the kernel marks the device as misbehaving and may pause it after repeated misses.

## Forbidden in the callback

| Operation | Why |
|---|---|
| `make([]T, n)` for hot-path-sized n | GC pause on allocation |
| `append` that may realloc | Same |
| `ch <- v` on an unbuffered or full channel | Blocks |
| `mutex.Lock()` on a contended mutex | Blocks |
| `fmt.Sprintf`, `errors.New`, `log.*` | Hidden allocations |
| `runtime.SetFinalizer`, `time.Now()` on Linux without VDSO | Variable cost |

## Allowed patterns

- Copy bytes into a pre-allocated buffer from a `sync.Pool`.
- Push into a lock-free SPSC ring (`internal/audio/ringbuf`).
- `select` with `default:` — non-blocking send.
- `atomic.*` counters.

## In this repo

- [[../../../internal/audio/capture/capture.go|capture.go]] writes via `unsafe.Slice` (no alloc) and calls the Sink.
- [[../../../internal/app/ring_adapters.go|ring_adapters.go]] `ringSink` calls `rb.Write` (0-alloc per design).
- [[../../../internal/audio/playback/playback.go|playback.go]] reads via `unsafe.Slice` and zeros the tail manually (no `make`).
- [[../../../internal/audio/vad/vad.go|vad.go]] `WriteFrame` is called from a *non* audio thread (consumer-side), so it allocates `Utterance.PCM`. The audio thread only writes to the ring; the segmenter runs on its own goroutine. **Important** — do not move VAD into the capture callback.

## Detection

`go test -bench=. -benchmem` on the hot-path types must report `0 B/op  0 allocs/op`. CI runs benchmarks but does not yet gate on allocs/op; we should add a `go test -run=^$ -bench=. -benchmem` regression test that fails if alloc count grows.

## Date discovered

2026-05-15 (M1 design).

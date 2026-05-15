---
title: "ADR-008: Piper TTS as subprocess (not cgo)"
date: 2026-05-15
tags:
  - decision
  - adr
  - accepted
  - tts
status: accepted
deciders: alex60217101990
---

# ADR-008: Piper TTS as subprocess (not cgo)

## Status

**Accepted** — 2026-05-15.

## Context

[[../../wiki/03-Models]] picked Piper for TTS in [[ADR-002 Piper over XTTS|ADR-002]]. M3b needs an implementation. Piper's C++ runtime is composed of three sub-libraries:

| Component | Role | Size | Build cost |
|---|---|---|---|
| piper itself | Glue, command-line, voice config parsing | ~50 kLOC C++ | low |
| piper-phonemize / espeak-ng | Grapheme-to-phoneme | ~600 kLOC C (espeak-ng) | medium |
| onnxruntime | Tensor inference | ~2 MLOC C++ | **very high** (30+ min, needs Python, vendor protobuf, …) |

Upstream Piper distributes pre-built binaries because building onnxruntime from source on a contributor machine is impractical. A cgo binding would either need:

1. Vendor onnxruntime source as a submodule and build it — pushes our first-time build from ~5 min to ~40 min.
2. Vendor pre-built onnxruntime tarballs per OS/arch — adds a download step that the rest of the project does at install time, not build time.
3. Use a third-party Go onnxruntime binding (e.g. `yalue/onnxruntime_go`) — that one still needs the user to have the `onnxruntime` shared library on the system, plus we'd need a Go phonemizer (not available in pure Go for the languages we care about).

None of these are quick wins for M3b. We need an end-to-end audio loop now so that the M3b milestone — the visible payoff of the project — can be demonstrated; the runtime detail can be refactored later.

## Decision

**Treat Piper as an external binary executable invoked via `os/exec`.**

Flow per utterance:

```
[pipeline goroutine] ← Translation, TargetLang
            │
            ▼
piper --model voice.onnx --output_raw            (stdin: text, stdout: 22050 Hz mono int16 PCM)
            │
            ▼
playback ringbuf  →  malgo playback device
```

Wrapper lives in [[../../../internal/tts/piper/piper.go]]; the binary path is auto-detected on `$PATH` and overridable with `--piper-bin`. Voices are registered manually via repeated `--voice lang=path` flags; the Models Manager UI in M5 will provide a graphical alternative.

## Consequences

Positive:

- M3b ships in days instead of weeks.
- Build matrix stays exactly the same — no new toolchains for any platform.
- User can swap Piper for any spec-compatible binary (mimic-3, espeak-ng-lite alternatives) by pointing `--piper-bin` at it.
- Failure to find piper degrades gracefully: STT+MT still work, only TTS is disabled.

Negative:

- **Per-utterance latency penalty**: ~30–50 ms cold-spawn overhead on macOS / Linux, up to 150 ms on Windows. Still within the 2 s p95 budget but a measurable tax we don't otherwise need.
- Distribution: end users must `brew install piper-tts` / `apt install piper` / download a Windows release. We will streamline this via a Models Manager UI download step (M5) — same model the app already uses for whisper/MT model files.
- Cannot stream long utterances in chunks. Piper has a `--json-input` mode where one long-lived process accepts many requests; we should adopt it before declaring this approach "done" for production.

## Migration plan

The wrapper public API is intentionally narrow:

```go
type Engine struct { ... }
func New(cfg Config) (*Engine, error)
func (e *Engine) AddVoice(v Voice) error
func (e *Engine) Synthesize(ctx context.Context, text, lang string) ([]int16, error)
```

A future cgo implementation of `Engine` keeps that interface. Steps when we do migrate:

1. Decide on a Go onnxruntime path — `yalue/onnxruntime_go` if it has matured, otherwise vendored cgo wrapper of `libonnxruntime.{a,so,dylib}` shipped as part of build-deps.
2. Vendor `piper-phonemize` and `espeak-ng` as submodules; build static archives.
3. Re-implement the `Engine` type with cgo. Tests in [[../../../internal/tts/piper/piper_test.go]] cover the surface; replace the fake-binary harness with one that loads a tiny test voice ONNX.

Adopt `--json-input` long-lived subprocess first (low risk, big latency win). Move to cgo only if profiling shows the subprocess overhead is the bottleneck.

## See also

- [[ADR-002 Piper over XTTS]]
- [[../libraries/Piper TTS]]
- [[../../wiki/03-Models]]

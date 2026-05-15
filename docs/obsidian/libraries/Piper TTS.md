---
title: Piper TTS
date: 2026-05-15
tags:
  - library
  - tts
  - subprocess
aliases:
  - piper
  - rhasspy/piper
---

# Piper TTS

Neural text-to-speech engine used as the synthesis backend.

## Why Piper

Per [[../decisions/ADR-002 Piper over XTTS]]: smallest CPU footprint (~63 MB per voice, ~150 ms RTF on a recent CPU), neutral but natural-sounding voices, MIT licensed, broad language coverage (40+).

## Integration

Per [[../decisions/ADR-008 Piper Subprocess|ADR-008]] we invoke Piper as a subprocess via `os/exec`. Wrapper: [[../../../internal/tts/piper/piper.go]].

```go
eng, _ := piper.New(piper.Config{BinaryPath: "/opt/homebrew/bin/piper"})
eng.AddVoice(piper.Voice{Lang: "en", ONNXPath: ".../en_US-amy-medium.onnx"})
pcm, _ := eng.Synthesize(ctx, "Hello, world.", "en")  // 22050 Hz mono int16
```

## Binary distribution

Piper ships as a single statically-linked executable per platform:

| OS | Channel | Install |
|---|---|---|
| macOS | Homebrew | `brew install piper-tts` |
| Linux | apt / AUR / GitHub releases | `apt install piper` |
| Windows | GitHub releases (.zip) | extract, add to PATH |

Self-contained: it bundles its own onnxruntime and espeak-ng. ~60 MB on disk.

## Voice files

Each voice is two files distributed together at `huggingface.co/rhasspy/piper-voices`:

| File | Contents |
|---|---|
| `xx_YY-name-quality.onnx` | ONNX model |
| `xx_YY-name-quality.onnx.json` | Sample rate, phoneme map, speaker id table, audio config |

The JSON sidecar must sit next to the `.onnx` file — Piper resolves it by appending `.json` to the model path. Our downloader fetches both in lockstep; see [[../../../internal/models/manifest/manifest.yaml]] `tts:` block.

## Sample rate quirk

| Quality preset | Sample rate |
|---|---|
| `x_low` | 16000 Hz |
| `low` | 16000 Hz |
| `medium` | **22050 Hz** (our default) |
| `high` | 22050 Hz |

We hard-code 22050 in [[../../../internal/tts/piper/piper.go#SampleRate]] and only register `medium`/`high` voices in the manifest. If a user manually points us at a `low` voice via `--voice` the playback rate will be wrong; that's a known limitation to fix when the Models Manager UI lands.

## CLI flags we use

```bash
piper --model voice.onnx --output_raw < text.txt > pcm.raw
```

- `--model`: voice `.onnx` path.
- `--output_raw`: emit raw little-endian int16 PCM to stdout (no WAV header).
- stdin: UTF-8 text, one phrase per line; we send one phrase per process.

Flags **not** used yet: `--json-input` (long-lived streaming mode, per [[../decisions/ADR-008 Piper Subprocess|ADR-008]] migration plan), `--speaker N` (multi-speaker voices), `--length-scale` (speed control).

## Latency profile

Measured on M-series mac with `en_US-amy-medium`:

| Phrase length | RTF | Wall time |
|---|---|---|
| "Hello." | 0.06 | 30 ms (process spawn dominates) |
| "Hello, how are you today?" | 0.10 | 150 ms |
| 20-word sentence | 0.18 | 700 ms |

Spawn overhead dominates short utterances. For sub-300-ms targets we will need `--json-input` mode.

## Pitfalls

> [!warning] Voice config required
> Forgetting to ship `<voice>.onnx.json` alongside `.onnx` makes piper exit silently with an unhelpful error. The Engine wrapper surfaces stderr in its return value.

> [!warning] Trailing odd byte
> Piper occasionally emits an unpaired zero byte at the very end of short utterances on macOS. `bytesToInt16` truncates the trailing odd byte rather than panicking.

## See also

- Upstream: https://github.com/rhasspy/piper
- Voices: https://huggingface.co/rhasspy/piper-voices
- Wrapper: [[../../../internal/tts/piper/piper.go]]
- [[../decisions/ADR-002 Piper over XTTS]]
- [[../decisions/ADR-008 Piper Subprocess]]
- [[../architecture/TTS Stage M3b]]

# Realtime Speech Translator

Local, real-time speech translator for desktop. The application captures
audio from your microphone, recognises speech via a streaming
[sherpa-onnx][sherpa] Zipformer, translates via SMaLL-100 or OPUS-MT,
synthesises the translation through sherpa's in-process Piper engine
(streaming PCM callback), and routes the synthesised audio into a
virtual microphone so any other application (Zoom, Discord, Google
Meet, Teams, OBS, …) picks it up as if it were your voice.

**Everything runs on-device.** No network calls except the first-launch
downloads of the model files.

| | |
|---|---|
| Platforms | macOS · Linux · Windows |
| STT       | streaming Zipformer transducer + Silero VAD |
| MT        | SMaLL-100 (one model, 100 languages) · OPUS-MT (per pair) |
| TTS       | sherpa-onnx Piper VITS, in-process, streaming callback |
| Licence   | Apache-2.0 (project), per-model licences listed below |

## Quick start

```bash
git clone https://github.com/alex60217101990/realtime-speech-translator.git
cd realtime-speech-translator
make build       # produces bin/translator
./bin/translator
```

The default build runs the STT + TTS pipeline with translation in
**passthrough** mode (source text is forwarded untouched). That is
enough to verify the audio path before installing the heavier MT
dependencies.

### Enabling translation

SMaLL-100 and OPUS-MT use CTranslate2 + SentencePiece, which must be
compiled out of the bundled submodules:

```bash
git submodule update --init --recursive
make deps        # builds SentencePiece + CTranslate2, ~10 min
make mt          # rebuilds bin/translator with -tags mt
```

The same binary now exposes both backends through the Settings tab.

## Native runtime

sherpa-onnx ships per-platform Go modules
(`sherpa-onnx-go-{macos,linux,windows}`) with the C-API libraries
embedded; `go build` pulls the right one automatically. There is no
manual download step for the realtime engines.

The probe target verifies linkage end-to-end:

```bash
make build
./bin/probe        # prints "sherpa-onnx C-API version: 1.13.2"
```

## Models

The application looks under `<data>/models/` for the following layout:

```
models/
├── stt/<name>/{encoder.onnx, decoder.onnx, joiner.onnx, tokens.txt}
├── vad/silero_vad.onnx
├── tts/<name>/{model.onnx, tokens.txt, espeak-ng-data/}
└── mt/
    ├── small100/{model.bin, config.json, sentencepiece.bpe.model}
    └── opusmt/<src>-<dst>/{model.bin, config.json, source.spm, target.spm}
```

`<data>` resolves to:

- macOS — `~/Library/Application Support/realtime-speech-translator`
- Linux — `$XDG_DATA_HOME/realtime-speech-translator` (default `~/.local/share/...`)
- Windows — `%LOCALAPPDATA%\realtime-speech-translator`

Models can be downloaded from the upstream sherpa-onnx model index at
<https://k2-fsa.github.io/sherpa/onnx/pretrained_models/index.html>.
The Settings tab picks up whatever subdirectories exist under
`models/stt/` and `models/tts/` and populates the dropdowns from
there.

## CLI helpers

| Command | Purpose |
|---------|---------|
| `cmd/translator` | full GUI app |
| `cmd/probe`      | print sherpa-onnx C-API version (linkage smoke) |
| `cmd/stt-mic`    | mic → streaming Zipformer → partials/finals to stdout |
| `cmd/tts-say`    | text → Piper → playback, prints latency to first chunk |

All four are built by `make build`.

## Architecture (1-line summary per stage)

```
mic ─▶ capture (S16 → SSE2 → F32) ─▶ stt (Zipformer + Silero VAD)
                                           │
                                  Partial / Final
                                           │
                                           ▼
                                   mt.Engine (Serial)
                                           │
                                    Translation
                                           │
                                           ▼
                                   tts (Piper, streaming PCM)
                                           │
                                           ▼
                                   playback (SPSC ring) ─▶ speaker / vmic
```

Every queue between stages is small and drops on overflow rather than
blocking the audio callback. The ringbuf is single-producer /
single-consumer, lock-free, cache-line-padded.

## Performance notes

- int16 ↔ float32 conversion is a Plan 9 SSE2 routine (8 samples per
  iteration). Benchmarks on Intel i7-9750H: **5×** speedup int→float,
  **10×** float→int over the scalar reference, zero allocations.
- Capture callback reinterprets the malgo byte buffer as `[]int16` via
  `unsafe.Slice` and the float32 working buffer is borrowed from a
  `sync.Pool` — steady-state capture is allocation-free.
- Playback callback reinterprets the device buffer as `[]float32` and
  reads from the ring with one `copy` per period.

## Testing

```bash
make test          # unit tests
make test-race     # race detector across the lot
make bench         # SIMD + ringbuf microbenchmarks
```

[sherpa]: https://github.com/k2-fsa/sherpa-onnx

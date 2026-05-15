# Realtime Speech Translator

Local, real-time speech translator for desktop. The application captures audio
from your microphone, recognises speech via Whisper, translates via MADLAD-400
(or OPUS-MT), synthesises the translation through Piper, and routes the
synthesised audio into a virtual microphone so any other application (Zoom,
Discord, Google Meet, Teams, OBS, …) picks it up as if it were your voice.

**Everything runs on-device.** No network calls except the first-launch
downloads of the model files.

| | |
|---|---|
| Platforms | macOS · Linux · Windows |
| Languages | 28 STT, 419 MT (MADLAD-400), 13 TTS voices |
| Licence | Apache-2.0 (project), per-model licences listed below |

## Quick start

```bash
git clone --recursive https://github.com/alex60217101990/realtime-speech-translator.git
cd realtime-speech-translator
make deps        # builds whisper.cpp, SentencePiece, CTranslate2 (~10 min first time)
make build       # produces bin/translator
./bin/translator
```

On first launch:

1. The application enumerates audio devices and looks for a virtual mic
   (BlackHole on macOS, VB-CABLE on Windows, PulseAudio null-sink on Linux).
   If none is present, a wizard walks you through installing the right one.
2. Open the **Models** tab and download the default set (Whisper-small,
   MADLAD-400-3B, a Piper voice for your target language). Each row in
   the Models Manager downloads everything that engine needs (e.g. MT
   rows fetch `model.bin` + `config.json` + `sentencepiece.model` in one
   shot). All files land in the per-OS data directory.
3. Pick source and target languages in **Settings**, then press **Start**
   on the **Main** tab. **Cmd/Ctrl + Space** toggles Start/Stop.

The next launch reads everything from the data dir automatically — **no
CLI flags required** as long as the Models tab has downloaded the matching
engine. CLI flags exist only for testing / overriding.

## Key bindings

| Key | Action |
|---|---|
| Cmd/Ctrl + Space | Start / Stop session |
| Tab | Move between widgets (Fyne default) |

## CLI flags

Most knobs live in **Settings**, but the CLI is handy for tests and one-offs:

```
--model <path>           Whisper ggml model (overrides Settings)
--src auto|en|ru|…       Source language
--dst en|ru|es|…         Target language
--mt madlad|opusmt|off   Translation backend
--mt-model <dir>         MADLAD CTranslate2 directory
--mt-spm <path>          MADLAD SentencePiece model
--opusmt-root <dir>      OPUS-MT models root (one subdir per {src}-{dst})
--tts                    Toggle Piper TTS (default: on)
--piper-bin <path>       Piper executable (default: $PATH lookup)
--voice lang=onnx        Register a Piper voice; repeat per language
--output-device <name>   Playback device substring (default: first detected virtual mic)
```

Environment:

| Variable | Default | Purpose |
|---|---|---|
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` — controls stderr verbosity. Log file is always at debug level. |
| `CGO_ENABLED` | `1` | Mandatory at build time; turned off implicitly on cross builds without a toolchain. |
| `GOEXPERIMENT` | `simd` | Optional; enables the AVX2-accelerated audio sample conversion path. |

## Files the application owns

| OS | Data | Config |
|---|---|---|
| macOS | `~/Library/Application Support/realtime-speech-translator/` | same |
| Linux | `~/.local/share/realtime-speech-translator/` | `~/.config/realtime-speech-translator/` |
| Windows | `%LOCALAPPDATA%\realtime-speech-translator\` | same |

Subdirectories:

- `models/whisper/ggml-*.bin`
- `models/mt/<name>/{model.bin, config.json, sentencepiece.bpe.model}`
- `models/tts/<voice>.onnx{,.json}`
- `translator.log` (5 MB rotating, 1 backup at `.1`)
- `crash-reports/*.txt` (pruned to 20 newest, written only on panic)
- `config.yaml`

## Virtual microphone

The headline feature requires a virtual audio device. The application does
not bundle one — instead it detects whichever you have installed and falls
back to a guided install on first launch. See
[`docs/wiki/04-Virtual-Audio.md`](docs/wiki/04-Virtual-Audio.md).

| OS | Recommended driver | Install |
|---|---|---|
| macOS | BlackHole 2ch (GPL-3) | `brew install --cask blackhole-2ch` |
| Linux | PulseAudio null sink (LGPL-2.1+) | the wizard runs `pactl load-module` for you |
| Windows | VB-CABLE (freeware) | download from <https://vb-audio.com/Cable/> |

After install, point your communication app's microphone setting at:

- macOS: **BlackHole 2ch**
- Linux: **Monitor of rstranslator_out**
- Windows: **CABLE Output (VB-Audio Virtual Cable)**

## Models we use

| Layer | Default model | Size | Licence |
|---|---|---|---|
| STT | `whisper-small` (ggml) | 466 MB | MIT |
| MT  | `madlad-400-3b-int8` (CTranslate2) | 1.6 GB | Apache-2.0 |
| TTS | Piper `medium`-quality voices | 60 MB each | MIT / CC-BY (per voice) |

Lightweight fallbacks (also in **Models** tab):

- `whisper-tiny` / `base` for slow machines.
- `m2m100-418m-int8` (MIT) as a smaller MT alternative.
- Piper `low` / `x_low` voices when CPU is constrained.

NLLB-200 is **not** shipped — it is licensed CC-BY-NC and would block
redistribution. See [`docs/obsidian/decisions/ADR-007 MADLAD over NLLB.md`](docs/obsidian/decisions/ADR-007%20MADLAD%20over%20NLLB.md).

## Building from source

```bash
# Submodules: whisper.cpp, sentencepiece, ctranslate2
git submodule update --init --recursive

# CMake-built C/C++ static libs (one-off, ~10 min)
make deps

# Go binary
make build         # bin/translator
make test          # unit + race tests
make bench         # microbenchmarks

# Per-OS installers
VERSION=0.1.0 make package
```

### GPU acceleration (Apple Silicon)

The default build is **CPU + BLAS only** because older Intel-mac AMD GPUs
break ggml-metal's matrix-multiply path and produce garbage transcripts.
Apple Silicon users can re-enable Metal:

```bash
rm -rf third_party/whisper.cpp/build_go
GGML_METAL=ON ./scripts/build-deps.sh whisper
make build
```

Verify the active backend in `translator.log` at startup — look for either
`using BLAS backend` (CPU default) or `using Metal backend` (GPU enabled).

CI runs the same scripts on every push. Tagged releases (`v*`) produce a
draft GitHub Release with macOS `.dmg`, Linux `.AppImage` + `.deb`, and a
Windows NSIS installer attached.

## Privacy

- No telemetry. Ever.
- Crash reports stay on your disk under `crash-reports/`. The **Help** menu
  has a "Reveal crash reports in Finder/Explorer/Files" item; uploading is
  manual.
- Model downloads connect to Hugging Face (`huggingface.co`) and GitHub
  releases. URLs are pinned in the embedded manifest.
- Audio never leaves your machine.

## Project layout

```
cmd/translator/         Fyne entry point
internal/app/           Session orchestrator, ringbuf adapters
internal/audio/         capture, playback, ringbuf, sample conv (SIMD), VAD
internal/stt/           pipeline + whisper.cpp wrapper
internal/mt/            engine + MADLAD / OPUS-MT backends, ct2/sp shims
internal/tts/piper/     Piper subprocess wrapper
internal/vmic/          virtual-mic detection + install guide
internal/models/        manifest, downloader, paths
internal/config/        YAML settings persistence
internal/ui/            Fyne screens (Models, Settings) + theme
internal/logging/       slog setup + file rotation
internal/crashreport/   panic capture
third_party/            CMake submodules (whisper.cpp, sentencepiece, ctranslate2)
build/                  OS-specific packaging templates (Info.plist, .desktop, NSIS)
scripts/                build-deps.sh + package-*.sh
docs/
├── wiki/               user-facing project wiki
└── obsidian/           engineering knowledge graph (Obsidian vault)
```

## Knowledge graph

`docs/obsidian/` is an Obsidian vault we maintain alongside the code. It
documents non-obvious decisions and gotchas that future contributors and
LLM-assisted sessions can pick up. Index lives at
[`docs/obsidian/index.md`](docs/obsidian/index.md).

You can also run the project through [graphify](https://github.com/safishamsi/graphify):

```bash
/graphify docs/obsidian/   # generates an interactive knowledge graph
```

## Contributing

Issues, bug reports, and patches welcome. Before opening a PR:

```bash
make vet
make test-race
```

Tests must pass with `-race`; `go vet` must be clean; commits should sit on
top of `main` and follow the existing `Mn: short summary` style.

## Licence

The project itself is Apache-2.0 — see [`LICENSE`](LICENSE).

Bundled / linked components have their own licences; the most important are
listed above. Each Piper voice you download carries its own attribution
in its `.onnx.json` sidecar — please respect those when you ship recordings
that contain synthesised audio.

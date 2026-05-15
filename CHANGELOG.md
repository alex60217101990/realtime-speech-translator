# Changelog

All notable changes to **realtime-speech-translator** are documented here.
This project follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Notes

- Nothing yet — see [v1.0.0](#100---2026-05-15) for the inaugural release.

---

## [1.0.0] - 2026-05-15

### Added

- **Audio capture and playback** via miniaudio (`malgo`), int16 mono with an
  AVX2-accelerated `int16↔float32` conversion path (Go 1.26 `simd/archsimd`).
- **Voice-activity segmentation** on top of WebRTC VAD with configurable
  aggressiveness, hangover, and pre-pad.
- **Speech-to-text** via `whisper.cpp` (vendored `v1.8.4` submodule), default
  `ggml-small` model with `tiny`/`base`/`medium`/`large` available in the
  Models tab. Streaming-friendly batch-per-utterance with rolling
  initial-prompt continuity.
- **Machine translation** through CTranslate2 + SentencePiece. Two backends:
  - `MADLAD-400-3B int8` (Apache-2.0, 419 languages, default).
  - per-pair OPUS-MT (Helsinki-NLP, Apache-2.0, lighter).
- **Text-to-speech** via the Piper subprocess wrapper (see
  [ADR-008](docs/obsidian/decisions/ADR-008%20Piper%20Subprocess.md)). 13
  voices ship in the manifest covering EN/RU/ES/DE/FR/PT/IT/ZH/UK/PL/NL/TR/JA.
- **Virtual microphone routing**: detection of BlackHole (macOS), VB-CABLE
  (Windows), PulseAudio null-sink / PipeWire loopback (Linux). First-launch
  install wizard with OS-specific steps; Linux auto-installs the null sink
  via `pactl`.
- **Fyne v2 desktop UI** with three tabs (Main / Models / Settings),
  Cmd/Ctrl + Space hotkey, light/dark/system themes, and live progress for
  model downloads.
- **Persistent settings** (`config.yaml` under
  XDG_CONFIG_HOME / AppData / Library/Application Support).
- **Resumable HTTP downloader** with `Range` resume, SHA-256 verification,
  and exponential-backoff retry. Pinned model URLs to Hugging Face + GitHub
  releases.
- **Structured logging** through `log/slog`: text to stderr at `LOG_LEVEL`,
  JSON to `<data>/translator.log` at debug, rotated at 5 MB.
- **Crash reporter** that captures panics into `<data>/crash-reports/`,
  rotated to keep the newest 20, then re-throws so the OS still sees a
  non-zero exit. Reports never leave disk unless the user explicitly
  uploads them.
- **Bluetooth HFP warning** in the Main tab when the OS forces a BT headset
  into low-rate mono SCO mode.
- **Packaging matrix**: macOS universal `.app` + `.dmg`, Linux AppImage +
  `.deb` (via `linuxdeploy` and `nfpm`), Windows NSIS installer. Both
  Apple notarisation and Windows `signtool` are wired in CI and gated on
  optional secrets.

### Architecture decisions accepted in v1.0

- **ADR-001 Desktop only** — drop iOS/Android because virtual-mic routing is
  impossible on locked-down mobile sandboxes.
- **ADR-002 Piper over XTTS** — favour the smaller, faster, neutral-voice
  engine; voice cloning deferred to a post-v1.0 high-quality mode.
- **ADR-004 Monolithic cgo binary** — single executable links every native
  library statically. No sidecar processes (Piper is the documented
  exception per ADR-008).
- **ADR-005 Fyne over Wails/Flutter** — pure-Go UI keeps the build and
  binary size in check.
- **ADR-006 Virtual mic guided install** — detect, never bundle; preserves
  Apache-2.0 compatibility and avoids redistribution constraints from
  BlackHole / VB-CABLE.
- **ADR-007 MADLAD-400 over NLLB-200** — Apache-2.0 default, 419 languages.
  NLLB removed because CC-BY-NC blocks redistribution.
- **ADR-008 Piper TTS as subprocess** — pragmatic shortcut to ship M3b
  without vendoring onnxruntime + espeak-ng. cgo migration plan documented.

### Tests and CI

- Unit tests with `-race` for ringbuf, sample conversion (portable + SIMD),
  VAD segmenter, downloader, manifest, config, logging, crash reporter,
  vmic, Piper subprocess wrapper.
- `go vet ./...` clean.
- GitHub Actions:
  - `build.yml` — vet + race tests + build on every push / PR for
    macOS / Linux / Windows.
  - `release.yml` — package matrix on `v*` tags or manual dispatch,
    publishes a draft GitHub Release with the four artifacts attached.

### Privacy

- No telemetry.
- No audio leaves the device.
- Network traffic is restricted to manifest-pinned model downloads on first
  launch (`huggingface.co`, `github.com`).

### Known limitations

- Audio thread is sometimes blocked by VAD's cgo `WebRtcVad_Process` call
  (~100 ns per 30 ms frame). Acceptable; documented in
  `gotchas/Audio Thread No Allocation.md`.
- Piper subprocess spawn adds ~30–50 ms per utterance on macOS / Linux,
  up to ~150 ms on Windows. Mitigation via `--json-input` long-lived mode
  is on the roadmap.
- The `--cluster-only` graphify path's MADLAD CTranslate2 model needs the
  upstream `ct2-transformers-converter` script to be re-run when Meta
  publishes a new MADLAD checkpoint.
- macOS `dmg` is plain UDZO; styled DMG layout deferred.
- ARM64 Linux + Windows builds not in the CI matrix yet.

### Roadmap beyond v1.0

- Sliding-window partial transcripts during ongoing utterances.
- Voice cloning via XTTS-v2 (opt-in high-quality mode).
- Auto-update mechanism (Sparkle / squirrel-equivalent).
- Distribution channels: Homebrew tap, AUR, Flatpak/Snap, WinGet.
- Multi-language UI (en/ru/es to start) backed by `internal/ui/i18n`.
- System-wide global hotkey (CGEventTap / RegisterHotKey).

[Unreleased]: https://github.com/alex60217101990/realtime-speech-translator/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/alex60217101990/realtime-speech-translator/releases/tag/v1.0.0

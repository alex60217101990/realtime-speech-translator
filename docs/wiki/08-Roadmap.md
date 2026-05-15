# 08 — Roadmap

## Обзор фаз

| Фаза | Заголовок | Длительность | Выход |
|---|---|---|---|
| M1 | Skeleton | 1 неделя | walking skeleton — capture+playback loopback |
| M2 | STT integration | 2 недели | whisper.cpp streaming, transcript в UI |
| M3 | MT + TTS | 2 недели | full pipeline на одной паре en↔ru |
| M4 | Virtual mic | 1 неделя | detection + guided install на 3 OS |
| M5 | Multi-lang + UI polish | 2 недели | 10+ языков, settings, models manager |
| M6 | Packaging | 1 неделя | подписанные релизы на 3 OS |
| M7 | Beta | 2 недели | багфиксы, тюнинг latency |
| M8 | v1.0 | — | стабильный релиз |

Итого до v1.0 — ~11 недель календарно (предполагая один разработчик full-time).

---

## M1 — Skeleton

**Цель**: walking skeleton — приложение запускается, есть UI, audio loop работает.

Задачи:
- [ ] `go.mod` с зависимостями (Fyne, malgo)
- [ ] `cmd/translator/main.go` — Fyne window, кнопка Start/Stop
- [ ] `internal/audio/capture` + `playback` — loopback без перевода (mic → playback)
- [ ] `internal/audio/ringbuf` — lock-free ringbuf для тестов
- [ ] Базовый Makefile, build на текущей ОС
- [ ] GitHub Actions: lint + test

DoD: на моей машине запускается, Start → слышу свой голос в наушниках через loopback.

---

## M2 — STT integration

**Цель**: streaming whisper.cpp в Go.

Задачи:
- [ ] `third_party/whisper.cpp` как git submodule
- [ ] `scripts/build-deps.sh` — статическая сборка whisper.cpp
- [ ] `internal/stt/whisper` — cgo wrapper, использует `whisper.cpp/bindings/go` или собственный
- [ ] `internal/audio/vad` — webrtcvad сегментация
- [ ] `internal/stt/stream.go` — окна, overlap, partial+final logic
- [ ] UI panel «Transcript» — обновление через binding
- [ ] Downloader для whisper-small
- [ ] Тесты: offline wav → ожидаемый transcript (золотые семплы)

DoD: говорю «привет», вижу partial и затем final transcript в UI; latency < 1s от конца фразы.

---

## M3 — MT + TTS

**Цель**: полный pipeline на en↔ru.

Задачи:
- [ ] `third_party/ctranslate2` submodule + build
- [ ] `internal/mt/ct2` — cgo wrapper (300–500 строк)
- [ ] `internal/mt/nllb.go` — language tags, контекст
- [ ] SentencePiece tokenizer через cgo
- [ ] `third_party/piper` submodule + build (или onnxruntime + piper-go)
- [ ] `internal/tts/piper` — cgo wrapper
- [ ] `internal/pipeline` — оркестратор, каналы, backpressure
- [ ] UI panel «Translation»
- [ ] Manager скачивает: nllb-200-distilled-int8, piper-en, piper-ru
- [ ] Latency benchmark: p95 < 2.0s

DoD: говорю по-русски → через ~1.5 секунды слышу английский в наушниках (через playback, без virtual mic ещё).

---

## M4 — Virtual mic

**Цель**: маршрутизация перевода в virtual mic на трёх OS.

Задачи:
- [ ] `internal/vmic/detect/macos.go` — CoreAudio device enumeration
- [ ] `internal/vmic/detect/linux.go` — pactl парсинг
- [ ] `internal/vmic/detect/windows.go` — WASAPI enumeration
- [ ] `internal/vmic/install/*.go` — guided install (открыть URL/installer)
- [ ] Setup wizard на первом запуске
- [ ] Settings: выбор output device, monitoring toggle
- [ ] Smoke-test: в Zoom test call виден наш virtual mic

DoD: переведённая речь слышна в Zoom test call как «голос пользователя».

---

## M5 — Multi-lang + UI polish

**Цель**: расширить набор языков, отшлифовать UI.

Задачи:
- [ ] +Piper голоса: es, de, fr, pt, it, zh, ja, ko (≥10 языков)
- [ ] Settings экран — Whisper model, threads, VAD aggressiveness
- [ ] Models manager — список, download/delete/update, disk usage
- [ ] Hotkey toggle (Cmd/Ctrl+Space)
- [ ] Темы Light/Dark
- [ ] UI локализация (en, ru)

DoD: можно выбрать любую пару из 10 языков и услышать перевод.

---

## M6 — Packaging

**Цель**: подписанные дистрибутивы на 3 OS.

Задачи:
- [ ] macOS .app, codesign + notarize, universal binary
- [ ] Linux AppImage + .deb
- [ ] Windows NSIS installer + signtool
- [ ] CI matrix билдит все 4 артефакта на каждом теге
- [ ] GitHub Release publishing pipeline

DoD: на каждом теге автоматически собираются `.dmg`, `.AppImage`, `.deb`, `.exe`.

---

## M7 — Beta

**Цель**: реальные пользователи, багфиксы.

Задачи:
- [ ] Crash reporter (locally only, opt-in upload)
- [ ] Логи в файлы, ротация
- [ ] Edge cases: BT codec degradation warning, sample rate mismatch
- [ ] Telemetry — **нет** (приватность first)
- [ ] Документация для пользователя (отдельный README, GIF демо)
- [ ] Issue templates на GitHub

DoD: ≥10 ранних пользователей, ≥5 closed issues.

---

## M8 — v1.0

**Цель**: стабильный релиз.

- [ ] Все известные блокеры fixed
- [ ] Latency p95 ≤ 2.0 s на референсной машине
- [ ] CHANGELOG.md
- [ ] Сайт-лендинг (опционально)

---

## Будущие фичи (post v1.0)

- Voice cloning (XTTS-v2) — high-quality режим
- Echo cancellation (WebRTC AEC) для monitoring через speakers
- GPU ускорение через Metal/CUDA/Vulkan
- iOS/Android (preview-only, без virtual mic)
- Push-to-translate hotkey
- Custom MT models (LoRA fine-tune для специфической терминологии)
- Plugin API для сторонних STT/MT/TTS backends
- Multi-speaker diarization

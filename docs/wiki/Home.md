# realtime-speech-translator — Wiki

> Локальное desktop-приложение для realtime-перевода речи с одного языка на другой с маршрутизацией результата в виртуальный микрофон.

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8)](https://go.dev/) [![License](https://img.shields.io/badge/license-Apache--2.0-blue)](./10-Licensing-Distribution.md) [![Platforms](https://img.shields.io/badge/platforms-macOS%20|%20Linux%20|%20Windows-lightgrey)]()

## Что это

Приложение слушает микрофон (включая Bluetooth-наушники), распознаёт речь, переводит её на выбранный язык и воспроизводит результат через виртуальный микрофон. Любое стороннее приложение — Zoom, Discord, Google Meet, Teams, OBS — видит этот виртуальный микрофон как обычный input device и получает уже переведённую речь.

Все модели работают **локально**. Никаких внешних API. Никакой телеметрии.

## Быстрый старт

```bash
git clone --recursive https://github.com/alex60217101990/realtime-speech-translator.git
cd realtime-speech-translator
make build
./bin/translator
```

На первом запуске:

1. Мастер настройки проверит наличие виртуального аудио-устройства.
2. Менеджер моделей предложит скачать default-набор (Whisper-small + NLLB-200-distilled + Piper-en/ru).
3. После загрузки — главный экран, выбор языковой пары, кнопка **Start**.

## Системные требования

| Компонент | Минимум | Рекомендуется |
|---|---|---|
| CPU | x86_64 с AVX2 или ARM64 | M-серия Apple / Ryzen 5+ / Core i5 12-го поколения |
| RAM | 4 GB | 8 GB |
| Disk | 3 GB на модели | 6 GB |
| OS | macOS 12+, Ubuntu 22.04+ / Fedora 38+, Windows 10 (1909+) | актуальные версии |
| Аудио-драйвер | BlackHole 2ch / PulseAudio / VB-CABLE | те же |

## Содержание

| Страница | Описание |
|---|---|
| [01-Architecture](./01-Architecture.md) | Компоненты, поток данных, goroutine-модель |
| [02-Audio-Pipeline](./02-Audio-Pipeline.md) | Capture → VAD → STT → MT → TTS → playback |
| [03-Models](./03-Models.md) | Whisper, NLLB-200, Piper; форматы; downloader |
| [04-Virtual-Audio](./04-Virtual-Audio.md) | BlackHole / PulseAudio / VB-CABLE |
| [05-UI-Fyne](./05-UI-Fyne.md) | Экраны, состояния, биндинги |
| [06-Build-and-Packaging](./06-Build-and-Packaging.md) | CGO, кросс-сборка, упаковка |
| [07-Performance](./07-Performance.md) | Бюджет ресурсов, профилирование |
| [08-Roadmap](./08-Roadmap.md) | Фазы развития M1 → v1.0 |
| [09-Testing](./09-Testing.md) | Unit, integration, e2e, latency, memory |
| [10-Licensing-Distribution](./10-Licensing-Distribution.md) | Лицензии моделей, драйверов, проекта |
| [11-Glossary](./11-Glossary.md) | Термины: VAD, RTF, ASR, ggml, ONNX и др. |

## Лицензия

Apache-2.0 (предварительно). Часть моделей под более ограничительными лицензиями — см. [10-Licensing-Distribution](./10-Licensing-Distribution.md).

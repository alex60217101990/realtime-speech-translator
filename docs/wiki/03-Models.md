# 03 — Модели

## Реестр (manifest.yaml)

Файл `internal/models/manifest/manifest.yaml` (бандлится в бинарник через `embed`):

```yaml
version: 1

whisper:
  tiny:
    url: https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-tiny.bin
    sha256: be07e048e1e599ad46341c8d2a135645097a538221678b7acdd1b1919c6e1b21
    size_mb: 39
  base:
    url: https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.bin
    sha256: 60ed5bc3dd14eea856493d334349b405782ddcaf0028d4b5df4088345fba2efe
    size_mb: 142
  small:
    url: https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.bin
    sha256: 1be3a9b2063867b937e64e2ec7483364a79917e157fa98c5d94b5c1fffea987b
    size_mb: 466
    default: true
  medium:
    url: https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-medium.bin
    sha256: 6c14d5adee5f86394037b4e4e8b59f1673b6cee10e3cf0b11bbdbee79c156208
    size_mb: 1500

mt:
  nllb-200-distilled-600M-int8:
    url: https://huggingface.co/JustFrederik/nllb-200-distilled-600M-ct2-int8/resolve/main/model.bin
    config_url: https://huggingface.co/JustFrederik/nllb-200-distilled-600M-ct2-int8/resolve/main/config.json
    tokenizer_url: https://huggingface.co/facebook/nllb-200-distilled-600M/resolve/main/sentencepiece.bpe.model
    sha256: <fill on first release>
    size_mb: 650
    default: true

tts:
  piper-en-US-amy-medium:
    onnx_url: https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/amy/medium/en_US-amy-medium.onnx
    json_url: https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/amy/medium/en_US-amy-medium.onnx.json
    size_mb: 63
    lang: en
  piper-ru-RU-irina-medium:
    onnx_url: https://huggingface.co/rhasspy/piper-voices/resolve/main/ru/ru_RU/irina/medium/ru_RU-irina-medium.onnx
    json_url: https://huggingface.co/rhasspy/piper-voices/resolve/main/ru/ru_RU/irina/medium/ru_RU-irina-medium.onnx.json
    size_mb: 63
    lang: ru
  # ... +es, +de, +fr, +pt, +it, +zh, +ja, +ko
```

> SHA256 для NLLB заполняется при первом релизе после проверки официального чексумма. Если автор не публикует — храним свой checksum и публикуем его в release notes.

## Путь хранения

| OS | Путь |
|---|---|
| macOS | `~/Library/Application Support/realtime-speech-translator/models/` |
| Linux | `$XDG_DATA_HOME/realtime-speech-translator/models/` (default `~/.local/share/`) |
| Windows | `%LOCALAPPDATA%\realtime-speech-translator\models\` |

Структура:

```
models/
├── whisper/
│   └── ggml-small.bin
├── mt/
│   └── nllb-200-distilled-600M-int8/
│       ├── model.bin
│       ├── config.json
│       └── sentencepiece.bpe.model
└── tts/
    ├── piper-en-US-amy-medium.onnx
    ├── piper-en-US-amy-medium.onnx.json
    └── ...
```

## Downloader (`internal/models/downloader`)

Требования:

- HTTP Range, **возобновляемая** загрузка (частичный файл `.part`).
- Прогресс через `chan Progress { Bytes int64; Total int64; SpeedBps float64 }`.
- Проверка SHA256 после загрузки. При ошибке — удалить, не оставлять corrupt-файл.
- Atomic rename `.part` → final.
- Параллельные сегменты — нет (HF rate-limit). Один поток на файл.
- Retry с exponential backoff: 1s → 2s → 4s → ... → max 60s, 5 попыток.

API:

```go
type Downloader interface {
    Fetch(ctx context.Context, m Model, dst string) (<-chan Progress, <-chan error)
    Verify(path string, sha256 string) error
}
```

## Квантизация

| Модель | Формат | Размер | Качество |
|---|---|---|---|
| Whisper small | ggml Q5_1 | 466 MB | стандартная |
| Whisper small | ggml Q8_0 | ~520 MB | чуть лучше |
| NLLB-200-distilled-600M | CT2 int8 | ~650 MB | -1 BLEU vs fp16 |
| Piper | ONNX fp32 (medium quality) | 60–80 MB | стандартная |

Можно публиковать `int4`-варианты Whisper позже для слабых машин.

## Lazy loading

- При `Session.Start()` модели mmap-ятся в RAM (whisper.cpp использует mmap; CT2 загружается полностью; Piper — ONNX-сессии).
- При `Session.Stop()` запускается таймер `IdleUnloadAfter` (5 min default).
- По таймауту — `Unload()` высвобождает RAM. Следующий Start снова mmap-ит.

## UI Models Manager

Отдельный экран показывает:

- Какие модели присутствуют локально / нужно скачать.
- Прогресс активных загрузок.
- Кнопки **Download** / **Update** / **Delete**.
- Общий disk-usage.

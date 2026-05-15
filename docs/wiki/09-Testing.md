# 09 — Тестирование

## Пирамида

```
        ┌─────────┐
        │   E2E   │   ~5 сценариев, golden audio → transcript+translation+wav
        ├─────────┤
        │  Integ  │  ~30 — STT/MT/TTS на эталонах, downloader, vmic detect
        ├─────────┤
        │  Unit   │  ~200 — ringbuf, VAD, lang mapping, config, etc.
        └─────────┘
```

## Unit

Покрытие ≥ 70% для:

- `internal/audio/ringbuf` — concurrent producer/consumer, переполнение, корректность wraparound.
- `internal/audio/vad` — детекция speech/silence на known frames, hangover logic, min/max utterance.
- `internal/models/manifest` — парсинг YAML, проверка обязательных полей.
- `internal/models/downloader` — resume from partial, sha256 mismatch detect, retry/backoff с подменённым clock.
- `internal/mt/nllb.go` — language tag mapping, контекст-prepend.
- `internal/config` — XDG paths, дефолты, miss-config errors.

Tooling: стандартный `testing`, `github.com/stretchr/testify` для assert, `testify/mock` где нужны моки.

## Integration

Каждый STT/MT/TTS компонент тестируется на эталонных входах:

### STT

Файлы `test/audio_samples/`:

- `ru_short_clean.wav` (16k, mono, 5s, женский голос, чистая запись)
- `ru_short_bt_codec.wav` (8k upsampled — имитация HFP)
- `en_long.wav` (60s беседы)
- `es_numbers.wav` (числительные, проверка робастности)

Тест:

```go
func TestWhisperGolden(t *testing.T) {
    for _, c := range cases {
        got := whisper.TranscribeFile(c.path)
        wer := jiwer.WER(c.expected, got)
        if wer > c.maxWER {
            t.Fatalf("%s: WER %.3f > %.3f", c.name, wer, c.maxWER)
        }
    }
}
```

### MT

Эталонные пары (~50 коротких реплик). Проверяем не точное совпадение, а **BLEU**:

```go
bleu := sacrebleu.Score(expected, got)
if bleu < 25.0 {  // NLLB distilled ожидание
    t.Fatal(...)
}
```

### TTS

Piper детерминирован при фиксированном seed. Эталон — sha256 от output PCM на канонических входах. Снимается raz, обновляется при upgrade модели.

### Downloader

Mock HTTP server отдаёт файл с симуляцией:

- Range requests
- 503 → retry
- partial bytes → resume
- corrupt body → sha256 mismatch

## E2E

Сценарии запускают **полный** pipeline в headless-режиме:

1. **«Простая фраза ru→en»**: WAV ru → ожидаем EN-транскрипт начинается с «hello» и WAV-out имеет длительность > 0.5s.
2. **«Длинная беседа en→es»**: 60s → ожидаем ≥ 10 utterances, p95 latency < 2.5s в тесте (релакс для CI runner).
3. **«Тишина»**: 30s silence → 0 utterances, 0 TTS playback.
4. **«Smoke первого запуска»**: пустая `~/.local/share/...`, downloader триггерится, по mock-CDN, скачивает tiny + 1 piper voice, успешно транскрибирует.
5. **«Recovery from STT crash»**: убиваем whisper context в середине session → pipeline должен показать error state, не паниковать, после Stop → Start → восстановиться.

E2E запускаются в `make test-e2e`, требуют наличие моделей (downloader или local cache). В CI — на nightly job-ах.

## Latency benchmarks

`./translator --bench` встроенный режим:

- Генерирует тестовый сигнал (sequence коротких реплик из TTS на source-языке).
- Замеряет: VAD-close → playback-write timestamp.
- Печатает p50/p95/p99 в ms, mean RAM, mean CPU.

Цель в CI:

| Метрика | Threshold |
|---|---|
| p95 latency (small model, macOS M-series CI) | ≤ 2200 ms |
| p99 latency | ≤ 3500 ms |
| Mean RAM | ≤ 2.2 GB |

## Memory leak tests

```bash
./translator --bench --duration=30m
```

Проверка через `gopsutil` или `ps -o rss`: RSS grow rate < 10 MB/мин. Если выше — leak в cgo (whisper/CT2/piper context не освобождается между utterances).

## Race detector

`go test -race ./...` обязательно перед PR merge. Учитывая активную работу с goroutines + cgo callback-ами, race detector ловит классические ошибки в каналах и shared state.

## Fuzzing

`go test -fuzz` для:

- VAD frame parsing (произвольные int16 sequences)
- Manifest YAML parser
- Language tag normalizer

## Coverage отчёт

```bash
go test -coverprofile=cover.out ./...
go tool cover -html=cover.out -o cover.html
```

Минимум: 60% общий; 70% для `internal/audio/`, `internal/models/`.

## Manual QA checklist (перед релизом)

- [ ] Start/Stop 50 раз подряд — без RAM growth
- [ ] BT гарнитура (AirPods + Sony WH-1000XM5) — нет крашей при разрыве/реконнекте
- [ ] Zoom test call — голос пользователя стабильно слышен
- [ ] OBS as source — записывает перевод
- [ ] Idle 30 мин → модели выгружены, RAM < 100 MB
- [ ] Сетевой обрыв во время downloader — recovery работает

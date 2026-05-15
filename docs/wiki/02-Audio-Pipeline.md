# 02 — Аудио-конвейер

## Формат сэмплов между ступенями

| Ступень | Sample rate | Каналы | Тип |
|---|---|---|---|
| Capture (mic) | 16000 Hz | mono | int16 |
| VAD frame | 16000 Hz | mono | int16, frame=30ms (480 sample) |
| STT input | 16000 Hz | mono | float32 (нормализовано на /32768) |
| MT | текст | — | string + lang tag |
| TTS output (Piper) | 22050 Hz | mono | int16 |
| Playback (vmic) | 48000 Hz | mono | int16 (ресемпл из 22050) |

Все ресемплы делаются через `libsamplerate` (через cgo `github.com/cocoonlife/goalsa` или собственный wrapper). Для realtime — `SRC_LINEAR` достаточен; для high-quality режима — `SRC_SINC_FASTEST`.

## Capture (`internal/audio/capture`)

```go
type Source struct {
    ctx     *malgo.AllocatedContext
    device  *malgo.Device
    out     chan<- []int16   // pre-allocated buffer pool
    pool    *sync.Pool
}
```

- Используем `malgo.DeviceTypeCapture` с `Format=FormatS16, Channels=1, SampleRate=16000`.
- Callback вызывается из C-потока — буфер копируется в `sync.Pool`-полученный slice (избегаем escape на heap).
- `runtime.LockOSThread()` на goroutine, читающую из callback-канала.
- Bluetooth: macOS автоматически переключает в HFP (8/16 kHz) при использовании mic, что снижает качество. Проверяем `device.Info.NativeDataFormats` и предупреждаем UI при degraded codec-е.

## VAD (`internal/audio/vad`)

WebRTC VAD через `github.com/maxhawkins/go-webrtcvad`. Параметры:

- `frame_size = 30ms` (480 samples @ 16kHz)
- `aggressiveness = 2` (0=lax, 3=strict)
- **Hangover**: после 300ms тишины — close utterance.
- **Min utterance**: 200ms (фильтруем короткие шумы).
- **Max utterance**: 15s (force-close, чтобы STT не висел).

```go
type Utterance struct {
    PCM      []int16        // sentence-bounded
    StartedAt time.Time
    DurationMs int
}
```

## STT streaming (`internal/stt/whisper`)

whisper.cpp поддерживает streaming через окна. Стратегия:

- **Окно**: 5s аудио (80000 sample @ 16kHz).
- **Overlap**: 1s между окнами для непрерывности (последнее предложение из предыдущего окна используется как `initial_prompt` для нового).
- **Partial**: после первой 1.5s — выдаём промежуточный transcript (для UI).
- **Final**: при close-сигнале от VAD или достижении max-окна — окончательный transcript.
- **Параметры whisper.cpp**: `n_threads=runtime.NumCPU()/2`, `temperature=0`, `no_speech_threshold=0.6`, `language=cfg.SourceLang`.

```go
type Transcript struct {
    Text      string
    IsFinal   bool
    StartedAt time.Time
    Lang      lang.Code
}
```

## MT (`internal/mt`)

CTranslate2 + NLLB-200-distilled-600M в int8.

- **Токенизатор**: SentencePiece (`github.com/eaigner/jsmin` нет — реализуем через cgo SP).
- **Языковые теги NLLB**: `eng_Latn`, `rus_Cyrl`, `spa_Latn`, и т.д. Маппинг в `internal/mt/nllb.go`.
- **Контекст**: prepend последние 1–2 final transcripts для consistency (~50 токенов).
- **Batch**: одиночный (realtime), beam_size=2.
- **Параметры**: `max_decoding_length=128`, `repetition_penalty=1.1`.

```go
type Translation struct {
    SourceText string
    TargetText string
    SourceLang lang.Code
    TargetLang lang.Code
}
```

## TTS (`internal/tts/piper`)

Piper — espeak-ng (фонемизация) + ONNX-модель (фонемы → mel → vocoder → PCM).

- **Голос на язык**: по дефолту скачиваем 1 голос на каждый поддерживаемый target.
- **Стриминг**: Piper генерирует PCM фразой целиком; для предложения 5–10 слов — ~150–400 ms на CPU.
- **Очередь**: FIFO `chan TTSJob` (cap=4). Если очередь полная — последняя translation дроп (warn в лог).

```go
type TTSJob struct {
    Text string
    Lang lang.Code
    Voice string
}
```

## Playback (`internal/audio/playback`)

- `malgo.DeviceTypePlayback`, target device — virtual mic (из `cfg.audio.output_device`).
- Опциональный monitoring: дублирующий sink на default-headphones, чтобы пользователь слышал перевод. **Внимание**: если monitoring включён и audio попадает обратно в mic — петля. Решения:
  - Headphones (закрытые) → нет петли.
  - Speakers → требуется echo-cancellation (вне MVP, см. roadmap).

## Бюджет латентности

```
mic capture        → 20 ms  (buffer)
VAD сегмент close  → 300 ms (hangover)
STT final          → 200–400 ms (small, CPU)
MT                 → 100–250 ms (NLLB int8, beam=2)
TTS Piper          → 150–400 ms (зависит от длины фразы)
Playback queue     → 20–50 ms
──────────────────────────
total target       ≤ 2000 ms end-to-end
```

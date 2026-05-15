# 07 — Performance budget и оптимизации

## Целевые показатели

| Метрика | Idle | Active session |
|---|---|---|
| RAM (RSS) | < 80 MB | < 2.0 GB |
| CPU (steady state) | < 1% | < 60% одного P-core |
| End-to-end latency | — | < 2.0 s p95 |
| Binary size | < 50 MB | — |
| Cold start | < 1.5 s до UI | — |
| First-translation latency после Start | < 3 s | — |

### Разложение RAM (active)

| Компонент | RAM |
|---|---|
| Go heap + UI (Fyne) | ~80 MB |
| Whisper small (mmap) | ~700 MB резидентно (но shared cache) |
| NLLB CT2 int8 | ~750 MB |
| Piper ONNX session | ~150 MB на голос (en+ru = 300 MB) |
| Audio буферы | < 5 MB |
| **Итого** | ~1.9 GB |

С tiny + один Piper → можно уложиться в ~1 GB.

## Оптимизации в Go-коде

### 1. Buffer pooling

```go
var pcmPool = sync.Pool{
    New: func() any {
        // 30ms @ 16kHz mono int16 = 960 byte
        return make([]int16, 480)
    },
}
```

Все аудио-фреймы берутся из pool, возвращаются после consume. Снижает GC pressure до ~0 в hot path.

### 2. Preallocation

При известной длине utterance — `make([]int16, 0, maxLen)`. Go 1.26 escape analysis уже размещает на stack буферы ≤ 32 KB (см. `go-stack-allocation` skill).

### 3. LockOSThread

Goroutine, читающая из audio callback, должна оставаться на одном OS-потоке для предсказуемой латентности и совместимости с CoreAudio.

```go
func (c *Source) run(ctx context.Context) {
    runtime.LockOSThread()
    defer runtime.UnlockOSThread()
    // ...
}
```

### 4. Численные трюки

- Конверсия int16 → float32 батчами по 16 элементов, используя `math.Float32frombits`-избегание.
- При наличии AVX2/NEON — `golang.org/x/sys/cpu` детект, fallback на portable.
- Ресемплинг 22050 → 48000 — целочисленный (160/441 рациональное), без float-арифметики в hot path.

### 5. Каналы вместо мьютексов

Везде, где возможно, используем `chan` для передачи владения, а не shared-state + Mutex.

## Профилирование

### pprof

```bash
./translator --pprof=:6060
go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30
go tool pprof http://localhost:6060/debug/pprof/heap
```

### Flight Recorder (Go 1.25+)

Для tail-latency спайков (моменты «один раз перевод занял 4 секунды»):

```go
fr := trace.NewFlightRecorder()
fr.SetSize(64 << 20)  // 64 MB rolling buffer
fr.Start()
// ...
// При обнаружении spike > 3.0s:
f, _ := os.Create("spike.trace")
fr.WriteTo(f)
```

Анализ — `go tool trace spike.trace`. См. skill `go-flight-recorder`.

### Benchmarks

```bash
go test -bench=. -benchmem -benchtime=10s ./internal/audio/...
go test -bench=. -benchmem ./internal/stt/...
```

Цель: 0 allocs/op в audio capture hot path; <1 alloc/utterance в STT wrapper.

## Идле-режим

После `Stop()`:

1. Через `IdleUnloadAfter` (5 min default):
   - Whisper context освобождён (`whisper_free`)
   - CT2 translator закрыт
   - ONNX session закрыта
   - Все буферы возвращены в pool
2. RAM падает к ~80 MB (только UI и Go runtime).
3. Следующий Start → `Load()` восстанавливает (mmap whisper ~150 ms, CT2 ~500 ms, Piper ~50 ms).

## Тепловой/энергетический profile

- На M1 active session = ~35–45% one P-core. Пассивное охлаждение, без вентилятора.
- На Ryzen 5 5600U — ~50–60% one core; вентилятор тихий.
- На Intel i5 12-го поколения — ~55%.

Не используем GPU по умолчанию (Metal/CUDA) — это +200 MB к binary и сложнее с лицензиями драйверов. Опция включить через `--gpu` для тех, кому нужно.

## CI guard

В test/e2e добавляем performance assertions:

```go
func TestLatencyP95(t *testing.T) {
    // load test/audio_samples/conversation_30s.wav
    // measure each utterance E2E
    if p95 > 2200*time.Millisecond {
        t.Fatalf("p95 latency %v > budget", p95)
    }
}
```

Запускается в nightly CI (не на каждом PR — runners шумят).

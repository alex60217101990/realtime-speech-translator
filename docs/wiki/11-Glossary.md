# 11 — Глоссарий

## Аудио

- **PCM** — Pulse-Code Modulation. Несжатый цифровой звук. У нас int16 mono 16/22050/48000 Hz.
- **Sample rate** — частота дискретизации. 16 kHz — стандарт для речевых моделей; 48 kHz — для virtual mic / playback.
- **Mono / Stereo** — 1 или 2 канала. Везде в пайплайне — mono.
- **Frame** — буфер фиксированной длины во времени. VAD оперирует 30ms-фреймами.
- **Utterance** — speech-сегмент от начала до конца речи (между VAD-паузами).
- **Buffer underrun / overrun** — нехватка/переполнение аудио-буфера; вызывает glitches.
- **DMA-буфер** — буфер между audio-устройством и приложением.

## Bluetooth

- **HFP** (Hands-Free Profile) — двунаправленный, низкое качество (8/16 kHz mono SCO). Активен когда BT-устройство и слушает, и говорит.
- **A2DP** (Advanced Audio Distribution Profile) — однонаправленный playback, высокое качество.
- **SBC / AAC / aptX / LDAC** — кодеки A2DP. SBC — обязательный baseline.
- Когда приложение запрашивает mic у BT-гарнитуры, OS переключается в HFP → playback качество тоже падает до 16 kHz mono. Это проблема UX, не наша.

## VAD / STT

- **VAD** — Voice Activity Detection. WebRTC VAD — стандарт.
- **ASR** — Automatic Speech Recognition (= STT).
- **WER** — Word Error Rate. Метрика качества ASR.
- **Beam search** — декодирование с топ-K candidates; trade-off скорость vs качество.
- **Streaming ASR** — выдача partial transcripts до окончания utterance.
- **Initial prompt** — текст-контекст, передаваемый в Whisper для biasing.

## Машинный перевод

- **MT** — Machine Translation.
- **BLEU** — метрика качества MT (0–100).
- **NMT** — Neural MT.
- **NLLB** — No Language Left Behind (Meta).
- **OPUS-MT** — семейство моделей Helsinki-NLP.
- **Distilled** — уменьшенная (knowledge distillation) версия модели.
- **SentencePiece** — токенизатор подсловами; BPE-вариант.
- **Beam size** — ширина beam search в декодере.

## TTS

- **TTS** — Text-To-Speech.
- **Phoneme** — звуковая единица языка. Piper использует espeak-ng для G2P (Grapheme-to-Phoneme).
- **Mel-spectrogram** — представление аудио в частотно-временной области.
- **Vocoder** — нейросеть, мел → waveform (в Piper встроен HiFi-GAN).
- **RTF** — Real-Time Factor. RTF=0.5 значит «1 секунда речи генерируется за 0.5s».

## Форматы / runtime

- **ggml** — формат тензоров whisper.cpp / llama.cpp. Q5_K, Q8_0 — квантизации.
- **ONNX** — Open Neural Network Exchange. Piper использует ONNX models.
- **CT2** — CTranslate2 binary format. Имеет int8 квантизацию.
- **Quantization** — снижение точности весов (fp32 → int8) для экономии RAM/ускорения CPU инференса.

## Go-специфичные

- **cgo** — Go ↔ C FFI.
- **goroutine** — легковесный thread в Go runtime.
- **Channel** — типизированный pipe для коммуникации между goroutines.
- **`runtime.LockOSThread`** — закрепляет goroutine за OS-потоком.
- **`sync.Pool`** — пул переиспользуемых объектов для снижения GC pressure.
- **Backpressure** — механизм притормаживания продьюсера, когда консьюмер не успевает.
- **Escape analysis** — компилятор Go решает, размещать ли значение на stack или heap.
- **mmap** — memory-mapped file; whisper.cpp использует mmap для модели → shared между процессами при необходимости.

## Архитектура

- **Backpressure** — см. выше.
- **Pipeline** — последовательность ступеней обработки, связанных каналами.
- **IPC** — Inter-Process Communication. У нас минимизировано (монолит), кроме `pactl` exec на Linux.
- **Hot path** — code path, исполняющийся часто (per frame / per sample). Аллокации тут запрещены.

## Виртуальное аудио

- **Virtual audio device** — программная имитация физического sound device.
- **Loopback** — output одного устройства = input другого (для маршрутизации).
- **Null sink** — sink, который не воспроизводит звук физически, но даёт monitor source.
- **HAL plugin** (macOS) — Hardware Abstraction Layer kernel extension для аудио.
- **WDM** — Windows Driver Model.
- **WASAPI** — Windows Audio Session API.
- **CoreAudio** — фреймворк аудио macOS.

## Прочее

- **Idle unload** — выгрузка моделей из RAM после периода неактивности.
- **First-translation latency** — время от Start до первого слышимого перевода.
- **End-to-end latency** — от произнесённого слова до его перевода в virtual mic.
- **Cold start** — время от запуска бинарника до первой возможности взаимодействовать.

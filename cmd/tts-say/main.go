// Command tts-say is a smoke test for internal/tts: synthesizes one
// argument string into PCM with sherpa-onnx Piper (VITS) and plays
// it through the default output device as chunks arrive.
//
// Usage:
//
//	go run ./cmd/tts-say \
//	  -model    ~/.../voice.onnx \
//	  -tokens   ~/.../tokens.txt \
//	  -data-dir ~/.../espeak-ng-data \
//	  -text     "Hello, streaming TTS."
//
// First audible audio should land < 500 ms after start on any modest
// CPU — that is the whole point of the streaming callback.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gen2brain/malgo"

	"github.com/alex60217101990/realtime-speech-translator/internal/tts"
)

func main() {
	model := flag.String("model", "", "path to Piper VITS model.onnx")
	tokens := flag.String("tokens", "", "path to tokens.txt")
	dataDir := flag.String("data-dir", "", "path to espeak-ng-data directory")
	text := flag.String("text", "Streaming text to speech is working.", "text to speak")
	speed := flag.Float64("speed", 1.0, "speech speed (1.0 nominal)")
	flag.Parse()

	if *model == "" || *tokens == "" || *dataDir == "" {
		flag.Usage()
		os.Exit(2)
	}

	cfg := tts.DefaultConfig()
	cfg.Model = *model
	cfg.Tokens = *tokens
	cfg.DataDir = *dataDir
	cfg.Speed = float32(*speed)

	engine, err := tts.New(cfg)
	if err != nil {
		log.Fatalf("tts init: %v", err)
	}
	defer engine.Close()

	sampleRate := engine.SampleRate()
	fmt.Printf("Piper model loaded. Sample rate: %d Hz.\n", sampleRate)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(string) {})
	if err != nil {
		log.Fatalf("malgo init: %v", err)
	}
	defer func() {
		_ = mctx.Uninit()
		mctx.Free()
	}()

	pending := make(chan []float32, 32)

	onPlayback := func(out, _ []byte, n uint32) {
		needSamples := int(n)
		want := needSamples * 4 // float32

		select {
		case chunk, ok := <-pending:
			if !ok {
				zero(out[:want])
				return
			}
			copySamples(out[:want], chunk, needSamples)
		default:
			zero(out[:want])
		}
	}

	deviceCfg := malgo.DefaultDeviceConfig(malgo.Playback)
	deviceCfg.Playback.Format = malgo.FormatF32
	deviceCfg.Playback.Channels = 1
	deviceCfg.SampleRate = uint32(sampleRate)
	deviceCfg.Alsa.NoMMap = 1

	device, err := malgo.InitDevice(mctx.Context, deviceCfg, malgo.DeviceCallbacks{Data: onPlayback})
	if err != nil {
		log.Fatalf("malgo device: %v", err)
	}
	defer device.Uninit()

	if err := device.Start(); err != nil {
		log.Fatalf("malgo start: %v", err)
	}

	start := time.Now()
	chunks := engine.Speak(ctx, *text)
	firstChunkReported := false

	for chunk := range chunks {
		if !firstChunkReported {
			fmt.Printf("First chunk: %v after Speak()\n", time.Since(start))
			firstChunkReported = true
		}
		select {
		case pending <- chunk.Samples:
		case <-ctx.Done():
			return
		}
	}

	close(pending)

	// Let the device drain whatever is still in the queue.
	tail := time.Duration(len(pending)) * time.Second / 4
	select {
	case <-ctx.Done():
	case <-time.After(tail + 500*time.Millisecond):
	}

	fmt.Printf("Done in %v\n", time.Since(start))
}

func copySamples(dst []byte, src []float32, n int) {
	count := n
	if count > len(src) {
		count = len(src)
	}
	for i := 0; i < count; i++ {
		binary.LittleEndian.PutUint32(dst[i*4:], math.Float32bits(src[i]))
	}
	if count < n {
		zero(dst[count*4 : n*4])
	}
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

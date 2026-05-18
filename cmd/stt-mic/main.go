// Command stt-mic is a smoke test for internal/stt: opens the default
// input device at 16 kHz mono, pipes audio into the streaming
// Zipformer + Silero VAD engine, and prints partials/finals to stdout.
//
// Usage:
//
//	go run ./cmd/stt-mic \
//	  -encoder  ~/.../encoder.onnx \
//	  -decoder  ~/.../decoder.onnx \
//	  -joiner   ~/.../joiner.onnx  \
//	  -tokens   ~/.../tokens.txt   \
//	  -vad      ~/.../silero_vad.onnx
//
// Ctrl-C to exit.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/gen2brain/malgo"

	"github.com/alex60217101990/realtime-speech-translator/internal/stt"
)

func main() {
	enc := flag.String("encoder", "", "path to streaming Zipformer encoder.onnx")
	dec := flag.String("decoder", "", "path to streaming Zipformer decoder.onnx")
	join := flag.String("joiner", "", "path to streaming Zipformer joiner.onnx")
	tok := flag.String("tokens", "", "path to tokens.txt")
	vad := flag.String("vad", "", "path to silero_vad.onnx")
	flag.Parse()

	if *enc == "" || *dec == "" || *join == "" || *tok == "" || *vad == "" {
		flag.Usage()
		os.Exit(2)
	}

	cfg := stt.DefaultConfig()
	cfg.Encoder = *enc
	cfg.Decoder = *dec
	cfg.Joiner = *join
	cfg.Tokens = *tok
	cfg.VADModel = *vad

	engine, err := stt.New(cfg)
	if err != nil {
		log.Fatalf("stt init: %v", err)
	}
	defer engine.Close()

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

	deviceCfg := malgo.DefaultDeviceConfig(malgo.Capture)
	deviceCfg.Capture.Format = malgo.FormatS16
	deviceCfg.Capture.Channels = 1
	deviceCfg.SampleRate = uint32(cfg.SampleRate)
	deviceCfg.Alsa.NoMMap = 1

	onFrames := func(_, in []byte, n uint32) {
		samples := int16BytesToFloat32(in[:int(n)*2])
		engine.Push(samples)
	}

	device, err := malgo.InitDevice(mctx.Context, deviceCfg, malgo.DeviceCallbacks{Data: onFrames})
	if err != nil {
		log.Fatalf("malgo device: %v", err)
	}
	defer device.Uninit()

	if err := device.Start(); err != nil {
		log.Fatalf("malgo start: %v", err)
	}

	go engine.Run(ctx)

	fmt.Println("Listening at 16 kHz mono. Speak — partials appear inline, finals on newline. Ctrl-C to exit.")

	for {
		select {
		case <-ctx.Done():
			fmt.Println()
			return
		case ev, ok := <-engine.Events():
			if !ok {
				return
			}
			switch e := ev.(type) {
			case stt.Partial:
				fmt.Printf("\r…  %s\033[K", e.Text)
			case stt.Final:
				fmt.Printf("\r✓  %s\n", e.Text)
			}
		}
	}
}

func int16BytesToFloat32(b []byte) []float32 {
	const scale = 1.0 / 32768.0
	n := len(b) / 2
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		s := int16(b[2*i]) | int16(b[2*i+1])<<8
		out[i] = float32(s) * scale
	}
	return out
}

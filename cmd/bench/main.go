// Command bench is the offline latency harness for the translator
// pipeline. It feeds a WAV file (or a generated sine sweep) into the
// VAD + Whisper + (optional) MT + (optional) Piper chain and reports
// per-utterance and aggregate p50/p95/p99 latency.
//
// Why a separate binary: the harness needs to wire the pipeline
// without opening malgo audio devices — those require hardware and
// human voices. CI smoke tests and `make bench-e2e` use this command
// instead.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"os"
	"slices"
	"time"

	"github.com/alex60217101990/realtime-speech-translator/internal/audio/sample"
	"github.com/alex60217101990/realtime-speech-translator/internal/audio/vad"
	"github.com/alex60217101990/realtime-speech-translator/internal/mt"
	"github.com/alex60217101990/realtime-speech-translator/internal/stt"
	"github.com/alex60217101990/realtime-speech-translator/internal/tts/piper"
)

func main() {
	wavPath := flag.String("wav", "", "input wav file (16 kHz mono int16). Empty = generate 30 s sine sweep.")
	modelPath := flag.String("model", "", "Whisper ggml model path (required)")
	srcLang := flag.String("src", "auto", "source language")
	dstLang := flag.String("dst", "", "target language; empty = STT only")
	mtModelDir := flag.String("mt-model", "", "MADLAD model dir")
	mtSPModel := flag.String("mt-spm", "", "MADLAD sentencepiece.model")
	piperBin := flag.String("piper-bin", "", "Piper binary path (empty = $PATH)")
	voicePath := flag.String("voice", "", "Piper voice .onnx for target lang")
	repeats := flag.Int("repeats", 1, "how many times to replay the input")
	flag.Parse()

	if *modelPath == "" {
		fmt.Fprintln(os.Stderr, "--model is required")
		os.Exit(2)
	}

	pcm, err := loadOrGeneratePCM(*wavPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load pcm:", err)
		os.Exit(1)
	}
	fmt.Printf("Input: %d samples (%.2f s @ 16 kHz mono)\n",
		len(pcm), float64(len(pcm))/16000.0)

	pcfg := stt.DefaultConfig()
	pcfg.Engine.Language = *srcLang

	if *dstLang != "" && *mtModelDir != "" && *mtSPModel != "" {
		eng, err := mt.NewMADLAD(mt.DefaultMADLADConfig(*mtModelDir, *mtSPModel))
		if err != nil {
			fmt.Fprintln(os.Stderr, "madlad:", err)
			os.Exit(1)
		}
		pcfg.MT = mt.Serial(eng)
		pcfg.TargetLang = *dstLang
	}

	if *voicePath != "" && *dstLang != "" {
		pcfg2 := piper.DefaultConfig()
		pcfg2.BinaryPath = *piperBin
		peng, err := piper.New(pcfg2)
		if err != nil {
			fmt.Fprintln(os.Stderr, "piper:", err)
		} else if err := peng.AddVoice(piper.Voice{Lang: *dstLang, ONNXPath: *voicePath}); err != nil {
			fmt.Fprintln(os.Stderr, "piper voice:", err)
		} else {
			pcfg.TTS = peng
		}
	}

	p, err := stt.New(*modelPath, pcfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pipeline:", err)
		os.Exit(1)
	}
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "start:", err)
		os.Exit(1)
	}

	collector := &collector{}
	go collector.run(p.Output())

	// Feed the PCM in 30 ms chunks at real time so the VAD segmenter
	// behaves the way it does in production.
	const chunk = vad.SamplesPerFrame
	for r := 0; r < *repeats; r++ {
		for off := 0; off+chunk <= len(pcm); off += chunk {
			p.WriteFrame(pcm[off : off+chunk])
			time.Sleep(time.Millisecond * 30) // soft real-time pacing
		}
	}

	// Allow the tail utterance to flush (hangover + STT pass).
	time.Sleep(2 * time.Second)
	cancel()
	collector.report()
}

// loadOrGeneratePCM either reads a minimal PCM-style WAV file or
// generates a 30 s sine-sweep stand-in if path is empty.
func loadOrGeneratePCM(path string) ([]int16, error) {
	if path == "" {
		return generateSineSweep(30 * 16000), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 44 {
		return nil, fmt.Errorf("file too short to be a WAV (%d bytes)", len(b))
	}
	// Trust the 44-byte canonical WAV header layout. Anything weirder
	// (RF64, JUNK chunks) belongs in a proper decoder, not here.
	pcmBytes := b[44:]
	if len(pcmBytes)%2 != 0 {
		pcmBytes = pcmBytes[:len(pcmBytes)-1]
	}
	pcm := make([]int16, len(pcmBytes)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(pcmBytes[i*2:]))
	}
	return pcm, nil
}

// generateSineSweep produces a placeholder PCM ramp from 200 Hz to
// 800 Hz over the requested sample count at amplitude 0.5. Plenty to
// trip the VAD on/off cycle.
func generateSineSweep(n int) []int16 {
	out := make([]int16, n)
	for i := range out {
		t := float64(i) / 16000.0
		freq := 200.0 + (600.0 * float64(i) / float64(n))
		out[i] = int16(math.Sin(2*math.Pi*freq*t) * 16000)
	}
	return out
}

// collector aggregates pipeline events and prints latency stats at
// the end of the run.
type collector struct {
	events []stt.Event
	wall   []time.Duration
	start  time.Time
}

func (c *collector) run(ch <-chan stt.Event) {
	c.start = time.Now()
	for ev := range ch {
		c.events = append(c.events, ev)
		c.wall = append(c.wall, time.Since(c.start))
	}
}

func (c *collector) report() {
	if len(c.events) == 0 {
		fmt.Println("No utterances produced — the VAD never opened.")
		return
	}
	stt := make([]time.Duration, 0, len(c.events))
	mt := make([]time.Duration, 0, len(c.events))
	tts := make([]time.Duration, 0, len(c.events))
	total := make([]time.Duration, 0, len(c.events))
	for _, ev := range c.events {
		stt = append(stt, ev.STTLatency)
		mt = append(mt, ev.MTLatency)
		tts = append(tts, ev.TTSLatency)
		total = append(total, ev.STTLatency+ev.MTLatency+ev.TTSLatency)
	}
	fmt.Printf("\nUtterances: %d\n", len(c.events))
	report("STT  ", stt)
	report("MT   ", mt)
	report("TTS  ", tts)
	report("TOTAL", total)

	fmt.Println("\nLast 3 transcripts:")
	tail := c.events
	if len(tail) > 3 {
		tail = tail[len(tail)-3:]
	}
	for _, ev := range tail {
		fmt.Printf("  [%s] %s\n", ev.Language, ev.Text)
		if ev.Translation != "" {
			fmt.Printf("  → [%s] %s\n", ev.TargetLang, ev.Translation)
		}
	}
}

func report(label string, samples []time.Duration) {
	if len(samples) == 0 {
		fmt.Printf("  %s: (none)\n", label)
		return
	}
	cp := make([]time.Duration, len(samples))
	copy(cp, samples)
	slices.Sort(cp)
	p50 := cp[len(cp)*50/100]
	p95 := cp[min(len(cp)*95/100, len(cp)-1)]
	p99 := cp[min(len(cp)*99/100, len(cp)-1)]
	var sum time.Duration
	for _, v := range cp {
		sum += v
	}
	mean := sum / time.Duration(len(cp))
	fmt.Printf("  %s: mean=%s p50=%s p95=%s p99=%s\n",
		label,
		mean.Round(time.Millisecond),
		p50.Round(time.Millisecond),
		p95.Round(time.Millisecond),
		p99.Round(time.Millisecond),
	)
}

// silence unused-import lint when only sample.Int16ToFloat32 happens
// to be missing from a partial build — bench reads PCM as int16 and
// hands it to the pipeline directly.
var _ = sample.Int16ToFloat32

package vad

import (
	"math"
	"testing"
)

// sine produces a 440 Hz sine wave at amplitude ~50% of full-scale, which
// the WebRTC VAD reliably classifies as speech-like.
func sine(nSamples int) []int16 {
	out := make([]int16, nSamples)
	for i := range out {
		out[i] = int16(math.Sin(2*math.Pi*440*float64(i)/SampleRate) * 16000)
	}
	return out
}

func silence(nSamples int) []int16 {
	return make([]int16, nSamples)
}

func TestSegmenterUtteranceDetected(t *testing.T) {
	s, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// 800 ms tone + 1000 ms silence => one utterance of ~800 ms.
	// Silence must exceed DefaultConfig.HangoverMs (800 ms) for the
	// segmenter to close the utterance and emit it.
	speechSamples := 800 * SampleRate / 1000
	silenceSamples := 1000 * SampleRate / 1000

	s.WriteFrame(sine(speechSamples))
	s.WriteFrame(silence(silenceSamples))

	select {
	case ut := <-s.Output():
		// Duration reports the active-speech time only; expect close to
		// the 800 ms input tone.
		if ut.Duration < 600*1e6 || ut.Duration > 1000*1e6 {
			t.Fatalf("unexpected duration: %v", ut.Duration)
		}
		if len(ut.PCM) == 0 {
			t.Fatal("empty PCM")
		}
	default:
		t.Fatal("no utterance produced")
	}
}

func TestSegmenterShortDropped(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinSpeechMs = 300
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// 100 ms speech is below MinSpeechMs and must be dropped.
	s.WriteFrame(sine(100 * SampleRate / 1000))
	s.WriteFrame(silence(500 * SampleRate / 1000))

	select {
	case <-s.Output():
		t.Fatal("short utterance was not dropped")
	default:
	}
}

func TestSegmenterMaxSpeechSplit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxSpeechMs = 500
	cfg.MinSpeechMs = 100
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// 1500 ms continuous tone — must produce at least one forced close
	// before the input ends (we feed silence after to flush).
	s.WriteFrame(sine(1500 * SampleRate / 1000))
	s.WriteFrame(silence(500 * SampleRate / 1000))

	got := 0
	for {
		select {
		case <-s.Output():
			got++
		default:
			if got < 2 {
				t.Fatalf("expected >=2 utterances for force-split, got %d", got)
			}
			return
		}
	}
}

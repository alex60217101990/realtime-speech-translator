package tts

import (
	"strings"
	"testing"
)

func TestNew_RejectsMissingPaths(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{name: "no model", cfg: Config{Tokens: "t", DataDir: "d"}},
		{name: "no tokens", cfg: Config{Model: "m", DataDir: "d"}},
		{name: "no data dir", cfg: Config{Model: "m", Tokens: "t"}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.cfg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "required") {
				t.Fatalf("error %q does not mention required paths", err)
			}
		})
	}
}

func TestDefaultConfig_PiperKnobs(t *testing.T) {
	c := DefaultConfig()
	if c.NoiseScale != 0.667 {
		t.Errorf("NoiseScale = %v, want 0.667 (Piper standard)", c.NoiseScale)
	}
	if c.NoiseScaleW != 0.8 {
		t.Errorf("NoiseScaleW = %v, want 0.8", c.NoiseScaleW)
	}
	if c.LengthScale != 1.0 {
		t.Errorf("LengthScale = %v, want 1.0", c.LengthScale)
	}
	if c.Speed != 1.0 {
		t.Errorf("Speed = %v, want 1.0", c.Speed)
	}
	if c.ChunkBuffer <= 0 {
		t.Error("ChunkBuffer must be positive so Speak does not deadlock on send")
	}
}

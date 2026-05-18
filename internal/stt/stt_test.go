package stt

import (
	"strings"
	"testing"
)

func TestNew_MissingPathsRejected(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "no encoder",
			cfg:  Config{Decoder: "d", Joiner: "j", Tokens: "t", VADModel: "v"},
			want: "encoder/decoder/joiner/tokens",
		},
		{
			name: "no vad",
			cfg:  Config{Encoder: "e", Decoder: "d", Joiner: "j", Tokens: "t"},
			want: "vad model",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.cfg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestDefaultConfig_SaneKnobs(t *testing.T) {
	c := DefaultConfig()
	if c.SampleRate != 16000 {
		t.Errorf("SampleRate = %d, want 16000", c.SampleRate)
	}
	if c.VADWindowSize != 512 {
		t.Errorf("VADWindowSize = %d, want 512 (Silero requirement)", c.VADWindowSize)
	}
	if c.Rule1MinTrailingSilenceSec <= 0 {
		t.Error("Rule1MinTrailingSilenceSec must be positive for endpoint detection")
	}
	if c.AudioBufferFrames <= 0 {
		t.Error("AudioBufferFrames must be positive to admit any audio")
	}
}

// TestEventTypes_ImplementEvent guarantees the public Event types stay
// type-switchable by external consumers.
func TestEventTypes_ImplementEvent(t *testing.T) {
	var _ Event = Partial{}
	var _ Event = Final{}
}

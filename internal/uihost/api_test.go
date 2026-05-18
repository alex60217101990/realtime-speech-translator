package uihost

import (
	"math"
	"testing"
)

func TestAudioBucket_RoundTrip(t *testing.T) {
	in := AudioBucket{
		SeqNo:     0xDEADBEEF,
		Rms:       0.42,
		Peak:      0.91,
		Bass:      0.30,
		Mid:       0.55,
		Treble:    0.18,
		VadActive: true,
		Speaking:  false,
	}
	buf, err := in.MarshalBinary()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if len(buf) != AudioBucketSize {
		t.Fatalf("Marshal returned %d bytes, want %d", len(buf), AudioBucketSize)
	}
	var out AudioBucket
	if err := out.UnmarshalBinary(buf); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.SeqNo != in.SeqNo {
		t.Errorf("SeqNo lost: got %x, want %x", out.SeqNo, in.SeqNo)
	}
	approx := func(a, b float32) bool { return math.Abs(float64(a-b)) < 1e-6 }
	for _, p := range []struct {
		name string
		a, b float32
	}{
		{"Rms", out.Rms, in.Rms},
		{"Peak", out.Peak, in.Peak},
		{"Bass", out.Bass, in.Bass},
		{"Mid", out.Mid, in.Mid},
		{"Treble", out.Treble, in.Treble},
	} {
		if !approx(p.a, p.b) {
			t.Errorf("%s lost: got %v, want %v", p.name, p.a, p.b)
		}
	}
	if out.VadActive != in.VadActive {
		t.Error("VadActive lost")
	}
	if out.Speaking != in.Speaking {
		t.Error("Speaking lost")
	}
}

func TestAudioBucket_TooShort(t *testing.T) {
	var b AudioBucket
	if err := b.UnmarshalBinary(make([]byte, 10)); err == nil {
		t.Fatal("expected error for short src, got nil")
	}
}

func TestEventKind_StableValues(t *testing.T) {
	// Wire values must not move; the UI hard-codes them.
	want := map[EventKind]uint8{
		EventInvalid:     0,
		EventPartial:     1,
		EventFinal:       2,
		EventTranslation: 3,
		EventSpeaking:    4,
		EventError:       5,
		EventLog:         6,
	}
	for k, v := range want {
		if uint8(k) != v {
			t.Errorf("EventKind %v changed wire value to %d, expected %d", k, uint8(k), v)
		}
	}
}

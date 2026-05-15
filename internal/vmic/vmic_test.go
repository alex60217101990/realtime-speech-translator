package vmic

import "testing"

func TestClassifyDarwin(t *testing.T) {
	cases := []struct {
		name string
		want Kind
	}{
		{"BlackHole 2ch", KindBlackHole},
		{"blackhole 16ch", KindBlackHole},
		{"Loopback Audio", KindLoopback},
		{"Soundflower (2ch)", KindSoundflower},
		{"MacBook Pro Speakers", KindUnknown},
	}
	for _, c := range cases {
		if got := classifyOn("darwin", c.name); got != c.want {
			t.Errorf("%q: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestClassifyLinux(t *testing.T) {
	cases := []struct {
		name string
		want Kind
	}{
		{"rstranslator_out", KindPulseNullSink},
		{"Null Output", KindPulseNullSink},
		{"pw-loopback Sink", KindPipeWireLoopback},
		{"Built-in Audio Analog Stereo", KindUnknown},
	}
	for _, c := range cases {
		if got := classifyOn("linux", c.name); got != c.want {
			t.Errorf("%q: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestClassifyWindows(t *testing.T) {
	cases := []struct {
		name string
		want Kind
	}{
		{"CABLE Input (VB-Audio Virtual Cable)", KindVBCable},
		{"CABLE-A Input (VB-Audio Cable A)", KindVBCableHiFi},
		{"VoiceMeeter Input (VB-Audio VoiceMeeter VAIO)", KindVoicemeeter},
		{"Realtek HD Audio Output", KindUnknown},
	}
	for _, c := range cases {
		if got := classifyOn("windows", c.name); got != c.want {
			t.Errorf("%q: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestGuideFields(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		g := guideFor(goos)
		if g.Title == "" {
			t.Errorf("%s: empty title", goos)
		}
		if len(g.Steps) == 0 {
			t.Errorf("%s: empty steps", goos)
		}
		if g.URL == "" && goos != "linux" {
			// Linux uses pactl, not a download URL; macOS/Windows must have one.
			t.Errorf("%s: missing URL", goos)
		}
	}
}

func TestSplitLines(t *testing.T) {
	got := splitLines("a\nb\n\nc")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("unexpected: %v", got)
	}
}

func TestSplitFields(t *testing.T) {
	got := splitFields("  42\tmodule-null-sink   sink_name=rstranslator_out  ")
	want := []string{"42", "module-null-sink", "sink_name=rstranslator_out"}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("idx %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestContains(t *testing.T) {
	if !contains("foo bar baz", "bar") {
		t.Fatal("expected bar")
	}
	if contains("foo bar", "qux") {
		t.Fatal("unexpected qux")
	}
	if !contains("anything", "") {
		t.Fatal("empty needle should match")
	}
}

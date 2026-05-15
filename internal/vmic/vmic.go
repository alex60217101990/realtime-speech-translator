// Package vmic locates and (where possible) creates the virtual-audio
// playback device that the translator writes synthesised speech into.
// Other applications consume the same device as a microphone input,
// which is the project's headline feature.
//
// Per-OS solutions:
//
//   macOS    BlackHole 2ch (Existential Audio, GPL-3) — installed via
//            `brew install --cask blackhole-2ch` or the upstream pkg.
//   Linux    PulseAudio / PipeWire null-sink module — we can load it
//            ourselves at runtime via the `pactl` CLI.
//   Windows  VB-CABLE (VB-Audio Software, freeware-with-conditions) —
//            user installs from vb-audio.com.
//
// Per ADR-001 we do not bundle any of these drivers; we detect them and
// guide the user through installation. See docs/obsidian/decisions/ADR-006
// Virtual Mic Guided Install.
package vmic

import (
	"runtime"
	"strings"
)

// Kind enumerates known virtual-audio backends. The set is open-ended:
// the application accepts any output device whose name matches a known
// pattern, plus a "Custom" override the user can pick manually.
type Kind int

const (
	KindUnknown Kind = iota
	KindBlackHole
	KindLoopback
	KindSoundflower
	KindPulseNullSink
	KindPipeWireLoopback
	KindVBCable
	KindVBCableHiFi
	KindVoicemeeter
	KindVAC // Virtual Audio Cable (paid)
)

func (k Kind) String() string {
	switch k {
	case KindBlackHole:
		return "BlackHole"
	case KindLoopback:
		return "Loopback (Rogue Amoeba)"
	case KindSoundflower:
		return "Soundflower"
	case KindPulseNullSink:
		return "PulseAudio null sink"
	case KindPipeWireLoopback:
		return "PipeWire loopback"
	case KindVBCable:
		return "VB-CABLE"
	case KindVBCableHiFi:
		return "VB-CABLE A+B"
	case KindVoicemeeter:
		return "Voicemeeter"
	case KindVAC:
		return "Virtual Audio Cable"
	}
	return "Unknown"
}

// Device pairs a Kind with the platform-specific name miniaudio
// reported for the device.
type Device struct {
	Kind Kind
	Name string
}

// pattern maps a name substring (case-insensitive) to a Kind. Order
// matters only for display sorting; matching itself is associative.
type pattern struct {
	needle string
	kind   Kind
}

// patternsFor returns the patterns we look for on the current OS. We
// expose this as a function so tests can drive detection with arbitrary
// fake device lists without touching runtime.GOOS.
func patternsFor(goos string) []pattern {
	switch goos {
	case "darwin":
		return []pattern{
			{"blackhole", KindBlackHole},
			{"loopback audio", KindLoopback},
			{"soundflower", KindSoundflower},
		}
	case "linux":
		return []pattern{
			{"rstranslator", KindPulseNullSink}, // our preferred sink
			{"null sink", KindPulseNullSink},
			{"null output", KindPulseNullSink},
			{"pw-loopback", KindPipeWireLoopback},
		}
	case "windows":
		return []pattern{
			{"cable input (vb-audio", KindVBCable},
			{"cable input", KindVBCable}, // less strict fallback
			{"cable-a input", KindVBCableHiFi},
			{"voicemeeter input", KindVoicemeeter},
			{"line 1 (virtual audio cable)", KindVAC},
		}
	}
	return nil
}

// classify returns the Kind a given device name maps to on the current
// OS, or KindUnknown if no pattern matches.
func classify(name string) Kind {
	return classifyOn(runtime.GOOS, name)
}

// classifyOn is the test-friendly version of classify; it accepts an
// explicit GOOS string.
func classifyOn(goos, name string) Kind {
	lower := strings.ToLower(name)
	for _, p := range patternsFor(goos) {
		if strings.Contains(lower, p.needle) {
			return p.kind
		}
	}
	return KindUnknown
}

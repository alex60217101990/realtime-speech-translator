// Package sample provides numeric conversion helpers for audio sample
// streams. The two hot-path operations are normalization of signed 16-bit
// PCM to float32 in [-1.0, 1.0) (input to STT models) and the reverse
// conversion with saturating clip (TTS output for playback).
//
// Build with GOEXPERIMENT=simd on amd64 to select the SIMD-accelerated
// implementation; otherwise a portable scalar version is used.
package sample

const (
	// invInt16Scale is 1.0 / 32768.0, applied to int16 samples to map them
	// into the conventional float32 range used by Whisper, NLLB and Piper.
	invInt16Scale float32 = 1.0 / 32768.0
)

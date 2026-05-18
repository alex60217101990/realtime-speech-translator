package capture

import "unsafe"

// unsafeBytesToInt16 reinterprets a byte slice as a slice of int16
// without copying. PCM S16LE matches the in-memory layout of int16
// on little-endian hosts (every platform we ship to: amd64 + arm64),
// so this is the documented zero-copy bridge between the malgo
// byte buffer and the SIMD conversion routine.
//
// The caller must guarantee len(b) is a multiple of 2 — the audio
// callback always passes a frame-aligned buffer, so the check stays
// at the call site.
func unsafeBytesToInt16(b []byte) []int16 {
	if len(b) == 0 {
		return nil
	}
	n := len(b) / 2
	return unsafe.Slice((*int16)(unsafe.Pointer(&b[0])), n)
}

// Package uihost is the cross-process contract between the Go
// engine layer and whichever UI front-end is currently bound to it
// (Fyne today; Wails v3 + React + Three.js per
// docs/UI_REWRITE_PLAN.md). The package is intentionally tiny — its
// whole job is to pin down a few wire types so the UI layer can be
// swapped without touching anything under internal/{stt,tts,mt,…}.
//
// Three flows cross the boundary:
//
//   1. Events  — Partial / Final / Translation / Speaking / Error /
//                LogLine. JSON-encoded one per Wails event message.
//   2. Stats   — periodic snapshot (1–4 Hz). JSON-encoded.
//   3. Audio   — AudioBucket frames (RMS + FFT bass/mid/treble +
//                VAD active flag), 32-byte binary, ~60 Hz. The
//                sphere shader subscribes to this stream.
//
// Binary AudioBucket avoids per-frame JSON allocations and keeps
// the bridge bandwidth at ~2 KB/s; everything else stays JSON for
// schema flexibility.
package uihost

import (
	"encoding/binary"
	"errors"
	"time"
)

// EventKind enumerates the event types the UI consumes. The numeric
// values are stable wire constants — never reorder.
type EventKind uint8

const (
	EventInvalid     EventKind = 0
	EventPartial     EventKind = 1
	EventFinal       EventKind = 2
	EventTranslation EventKind = 3
	EventSpeaking    EventKind = 4
	EventError       EventKind = 5
	EventLog         EventKind = 6
)

// Event is the JSON envelope every UI event uses. Fields are
// populated per Kind.
type Event struct {
	// Schema version — bumped on incompatible field changes so a
	// stale UI can refuse to render rather than guess. The UI may
	// inspect this and refresh itself.
	Schema int `json:"v"`

	// Monotonic id assigned by the host. Lets the UI pair a Final
	// with its later Translation (out-of-order arrival possible
	// when MT is slow). 0 on Partials / Log / Speaking.
	ID uint64 `json:"id,omitempty"`

	// Wall-clock at host. UI shows local-formatted time and may
	// derive lag from (now - At).
	At time.Time `json:"at"`

	// Discriminator. Wire-stable integer; see EventKind constants.
	Kind EventKind `json:"kind"`

	// Free-form text. Used by Partial / Final / Translation / Log
	// / Error. Empty for Speaking.
	Text string `json:"text,omitempty"`

	// Used by Translation events: language of the Source text the
	// translation was produced from, and the target language.
	SrcLang string `json:"src_lang,omitempty"`
	TgtLang string `json:"tgt_lang,omitempty"`
	// SourceText carries the original Final this Translation pairs
	// with (helpful when the UI lost ID context after a reload).
	SourceText string `json:"source_text,omitempty"`

	// Used by Speaking: true on start, false on stop.
	Speaking bool `json:"speaking,omitempty"`

	// Used by Log: structured level + key/value pairs flattened to
	// a single human-readable string. Keep it ASCII-safe for the
	// terminal pane.
	Level string `json:"level,omitempty"` // "DEBUG" / "INFO" / "WARN" / "ERROR"
}

// CurrentSchema is the version the host emits. The UI compares
// every event against this and treats a mismatch as "renderer too
// old, refresh".
const CurrentSchema = 1

// ----------------------------------------------------------------------------
// Stats — periodic snapshot the UI displays in the status block.
// ----------------------------------------------------------------------------

// Stats mirrors rstapp.Stats but with explicit JSON tags so the
// shape is stable across rewrites.
type Stats struct {
	Schema int `json:"v"`

	Running         bool    `json:"running"`
	CapturedSec     float64 `json:"captured_sec"`
	MicRMS          float32 `json:"mic_rms"`
	MicPct          int     `json:"mic_pct"`
	VADActivePct    int     `json:"vad_active_pct"`
	Utterances      uint64  `json:"utts"`
	STTDropped      uint64  `json:"stt_drop"`
	MTDropped       uint64  `json:"mt_drop"`
	TTSDropped      uint64  `json:"tts_drop"`
	CaptureDropped  uint64  `json:"cap_drop"`
	PlaybackUnderrn uint64  `json:"pb_under"`
	MicMutedDropped uint64  `json:"mic_muted"`

	MTLastMs  int64 `json:"mt_last_ms"`
	TTSLastMs int64 `json:"tts_last_ms"`

	MTEngine  string `json:"mt_engine"`
	MTEnabled bool   `json:"mt_enabled"`

	TMSize    int     `json:"tm_size"`
	TMPinned  uint64  `json:"tm_pinned"`
	TMHits    uint64  `json:"tm_hits"`
	TMMisses  uint64  `json:"tm_misses"`
	TMHitRate float64 `json:"tm_hit_rate"`
}

// ----------------------------------------------------------------------------
// AudioBucket — binary frame for the audio-reactive sphere shader.
// ----------------------------------------------------------------------------

// AudioBucketSize is the on-wire size of an AudioBucket in bytes.
// Stable; do not change.
const AudioBucketSize = 32

// AudioBucket is one snapshot of mic audio for the sphere.
//
//	bytes  offset  field
//	0..3      0    SeqNo  uint32   (LE)
//	4..7      4    Rms    float32  (0..1)
//	8..11     8    Peak   float32  (0..1)
//	12..15   12    Bass   float32  (0..1, 20–250 Hz energy)
//	16..19   16    Mid    float32  (0..1, 250–4000 Hz energy)
//	20..23   20    Treble float32  (0..1, 4000–8000 Hz energy)
//	24       24    VadActive uint8 (0 / 1)
//	25       25    Speaking  uint8 (TTS playing — 0 / 1)
//	26..31         reserved
//
// At 60 Hz the wire bandwidth is 32 · 60 = ~2 KB/s, well inside any
// IPC link's noise floor. Endianness is little-endian everywhere we
// ship (amd64 + arm64).
type AudioBucket struct {
	SeqNo     uint32
	Rms       float32
	Peak      float32
	Bass      float32
	Mid       float32
	Treble    float32
	VadActive bool
	Speaking  bool
}

// MarshalBinary encodes the bucket into a fixed-size little-endian
// byte slice. Allocation-free for the caller if it reuses the
// returned slice. (Each call allocates a fresh 32-byte slice; the
// optimiser inlines the put calls.)
func (b AudioBucket) MarshalBinary() ([]byte, error) {
	buf := make([]byte, AudioBucketSize)
	b.PutBinary(buf)
	return buf, nil
}

// PutBinary writes the bucket into dst in place. dst must be at
// least AudioBucketSize bytes long.
func (b AudioBucket) PutBinary(dst []byte) {
	if len(dst) < AudioBucketSize {
		panic("uihost: AudioBucket.PutBinary dst too short")
	}
	binary.LittleEndian.PutUint32(dst[0:], b.SeqNo)
	binary.LittleEndian.PutUint32(dst[4:], float32bits(b.Rms))
	binary.LittleEndian.PutUint32(dst[8:], float32bits(b.Peak))
	binary.LittleEndian.PutUint32(dst[12:], float32bits(b.Bass))
	binary.LittleEndian.PutUint32(dst[16:], float32bits(b.Mid))
	binary.LittleEndian.PutUint32(dst[20:], float32bits(b.Treble))
	dst[24] = boolByte(b.VadActive)
	dst[25] = boolByte(b.Speaking)
	for i := 26; i < AudioBucketSize; i++ {
		dst[i] = 0
	}
}

// UnmarshalBinary decodes a single bucket. Returns an error if src
// is shorter than AudioBucketSize.
func (b *AudioBucket) UnmarshalBinary(src []byte) error {
	if len(src) < AudioBucketSize {
		return errors.New("uihost: AudioBucket too short")
	}
	b.SeqNo = binary.LittleEndian.Uint32(src[0:])
	b.Rms = bitsToFloat32(binary.LittleEndian.Uint32(src[4:]))
	b.Peak = bitsToFloat32(binary.LittleEndian.Uint32(src[8:]))
	b.Bass = bitsToFloat32(binary.LittleEndian.Uint32(src[12:]))
	b.Mid = bitsToFloat32(binary.LittleEndian.Uint32(src[16:]))
	b.Treble = bitsToFloat32(binary.LittleEndian.Uint32(src[20:]))
	b.VadActive = src[24] != 0
	b.Speaking = src[25] != 0
	return nil
}

// helpers — keep the binary I/O free of math.Float32bits import
// noise at the top of the file.

func float32bits(f float32) uint32 {
	return *(*uint32)(unsafePtrOf(&f))
}

func bitsToFloat32(b uint32) float32 {
	return *(*float32)(unsafePtrOf(&b))
}

func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

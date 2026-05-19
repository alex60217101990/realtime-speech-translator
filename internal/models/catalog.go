// Package models is the hand-maintained catalog of downloadable
// engine artefacts and the installer that fetches + unpacks them
// into the per-OS data directory.
//
// The catalog is intentionally a hard-coded Go slice rather than a
// YAML manifest:
//
//   - the upstream URLs and archive layouts are stable; they change
//     once or twice a year and a code review is the right place to
//     re-verify the SHA / filename mapping.
//   - the layout transform (rename encoder-epoch-…onnx →
//     encoder.onnx, etc.) is per-entry logic, not data.
//   - a Go-coded catalog stays in sync with the engine APIs at
//     compile time — no missing-key runtime surprises.
package models

import (
	"github.com/alex60217101990/realtime-speech-translator/internal/paths"
)

// Kind tags an entry with the engine layer it belongs to. Used by
// the UI to group rows.
type Kind string

const (
	KindSTT Kind = "stt"
	KindVAD Kind = "vad"
	KindTTS Kind = "tts"
	KindMT  Kind = "mt"
)

// Entry describes one downloadable model artefact. The Installer
// uses URL + Tarball + Layout; the UI uses DisplayName + Kind +
// SizeBytes + RequiredFiles (for the install-status badge).
type Entry struct {
	Kind        Kind
	Name        string // directory name under models/<kind>/
	DisplayName string
	License     string
	URL         string
	SizeBytes   int64

	// Tarball is true when URL points at a .tar.bz2 / .tar.gz that
	// the installer must unpack. When false the URL is a single
	// file copied verbatim to the install dir.
	Tarball bool

	// Layout maps archive path → final file name inside the install
	// directory. For single-file entries use one entry with src="".
	// A src that ends with "/" or that resolves to a directory in
	// the archive is recursively copied.
	Layout map[string]string

	// RequiredFiles are the install-dir-relative paths that must
	// exist for the entry to count as installed.
	RequiredFiles []string

	// BackendName is the value the cmd layer should write into
	// config.MTBackend when this MT entry is installed (e.g.
	// "m2m100", "small100", "opusmt"). Ignored for non-MT entries.
	BackendName string

	// pathFn yields the install root; nil for entries that map onto
	// the canonical paths.STTDir / paths.TTSDir / paths.VADModel /
	// paths.MTDir directories (default behaviour, see InstallDir).
	pathFn func() (string, error)
}

// InstallDir returns the canonical install directory for the entry,
// creating intermediate directories on demand.
func (e Entry) InstallDir() (string, error) {
	if e.pathFn != nil {
		return e.pathFn()
	}
	switch e.Kind {
	case KindSTT:
		return paths.STTDir(e.Name)
	case KindTTS:
		return paths.TTSDir(e.Name)
	case KindMT:
		return paths.MTDir(e.Name)
	case KindVAD:
		// Silero lives at <models>/vad/silero_vad.onnx — return the
		// parent directory and let the installer write the file at
		// the leaf name supplied by Layout.
		p, err := paths.VADModel()
		if err != nil {
			return "", err
		}
		return dirOf(p), nil
	default:
		return "", errBadKind
	}
}

// DefaultCatalog returns the current curated list. Adding a model
// here is the one and only place a new download needs to register.
func DefaultCatalog() []Entry {
	return []Entry{
		{
			Kind:        KindVAD,
			Name:        "silero",
			DisplayName: "Silero VAD",
			License:     "MIT",
			URL:         "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/silero_vad.onnx",
			SizeBytes:   643_854,
			Tarball:     false,
			Layout:      map[string]string{"": "silero_vad.onnx"},
			RequiredFiles: []string{
				"silero_vad.onnx",
			},
		},
		{
			Kind:        KindSTT,
			Name:        "zipformer-streaming-en",
			DisplayName: "Streaming Zipformer (English)",
			License:     "Apache-2.0",
			URL:         "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-streaming-zipformer-en-2023-06-26.tar.bz2",
			SizeBytes:   310_414_022,
			Tarball:     true,
			Layout: map[string]string{
				"encoder-epoch-99-avg-1-chunk-16-left-128.onnx": "encoder.onnx",
				"decoder-epoch-99-avg-1-chunk-16-left-128.onnx": "decoder.onnx",
				"joiner-epoch-99-avg-1-chunk-16-left-128.onnx":  "joiner.onnx",
				"tokens.txt": "tokens.txt",
			},
			RequiredFiles: []string{
				"encoder.onnx", "decoder.onnx", "joiner.onnx", "tokens.txt",
			},
		},
		{
			Kind:        KindSTT,
			Name:        "streaming-t-one-russian",
			DisplayName: "T-one Streaming (Russian, telephony)",
			License:     "Apache-2.0 (Voicekit T-Software DC)",
			URL:         "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-streaming-t-one-russian-2025-09-08.tar.bz2",
			SizeBytes:   128_468_156,
			Tarball:     true,
			Layout: map[string]string{
				"model.onnx": "model.onnx",
				"tokens.txt": "tokens.txt",
			},
			RequiredFiles: []string{
				"model.onnx", "tokens.txt",
			},
		},
		{
			Kind:        KindSTT,
			Name:        "nemo-ctc-punct-giga-am-v3-russian",
			DisplayName: "NeMo GigaAM v3 + Punct (Russian, offline)",
			License:     "Free (Sber AI · GigaAM v3, Dec 2025)",
			URL:         "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-ctc-punct-giga-am-v3-russian-2025-12-16.tar.bz2",
			SizeBytes:   163_286_197,
			Tarball:     true,
			Layout: map[string]string{
				"model.int8.onnx": "model.int8.onnx",
				"tokens.txt":      "tokens.txt",
			},
			RequiredFiles: []string{
				"model.int8.onnx", "tokens.txt",
			},
		},
		{
			Kind:        KindTTS,
			Name:        "piper-en-amy-low",
			DisplayName: "Piper en_US Amy (low, English)",
			License:     "MIT (Piper) + GPL-3.0 (espeak-ng)",
			URL:         "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/vits-piper-en_US-amy-low.tar.bz2",
			SizeBytes:   67_095_344,
			Tarball:     true,
			Layout: map[string]string{
				"en_US-amy-low.onnx": "model.onnx",
				"tokens.txt":         "tokens.txt",
				"espeak-ng-data":     "espeak-ng-data",
			},
			RequiredFiles: []string{
				"model.onnx", "tokens.txt", "espeak-ng-data",
			},
		},
		{
			Kind:        KindTTS,
			Name:        "piper-ru-irina-medium",
			DisplayName: "Piper ru_RU Irina (medium, Russian)",
			License:     "MIT (Piper) + GPL-3.0 (espeak-ng)",
			URL:         "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/vits-piper-ru_RU-irina-medium.tar.bz2",
			SizeBytes:   67_153_308,
			Tarball:     true,
			Layout: map[string]string{
				"ru_RU-irina-medium.onnx": "model.onnx",
				"tokens.txt":              "tokens.txt",
				"espeak-ng-data":          "espeak-ng-data",
			},
			RequiredFiles: []string{
				"model.onnx", "tokens.txt", "espeak-ng-data",
			},
		},
		{
			Kind:        KindMT,
			Name:        "m2m100-418m-int8",
			DisplayName: "M2M-100 418M int8 (100 languages, CT2)",
			License:     "MIT (Facebook M2M-100)",
			URL:         "https://github.com/alex60217101990/realtime-speech-translator/releases/download/models-v1/m2m100-418m-int8.tar.gz",
			SizeBytes:   460_864_138,
			Tarball:     true,
			BackendName: "m2m100",
			Layout: map[string]string{
				"model.bin":               "model.bin",
				"config.json":             "config.json",
				"shared_vocabulary.json":  "shared_vocabulary.json",
				"sentencepiece.bpe.model": "sentencepiece.bpe.model",
			},
			RequiredFiles: []string{
				"model.bin", "config.json", "sentencepiece.bpe.model",
			},
		},
		{
			// SMaLL-100 is a distilled M2M-100 (~330 MB int8). The MT
			// loader (internal/mt/small100.go) supports it, but we
			// don't yet host a CT2-exported tarball — the UI shows
			// this entry as "URL TBD" so the user can see the
			// backend is wired and ready for a future release.
			Kind:        KindMT,
			Name:        "small100-int8",
			DisplayName: "SMaLL-100 int8 (distilled M2M-100, CT2)",
			License:     "MIT (Microsoft SMaLL-100)",
			URL:         "",
			SizeBytes:   330_000_000,
			Tarball:     true,
			BackendName: "small100",
			Layout: map[string]string{
				"model.bin":               "model.bin",
				"config.json":             "config.json",
				"shared_vocabulary.json":  "shared_vocabulary.json",
				"sentencepiece.bpe.model": "sentencepiece.bpe.model",
			},
			RequiredFiles: []string{
				"model.bin", "config.json", "sentencepiece.bpe.model",
			},
		},
	}
}

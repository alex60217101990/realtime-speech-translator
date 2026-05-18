//go:build mt

package mt

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/alex60217101990/realtime-speech-translator/internal/mt/ct2"
	"github.com/alex60217101990/realtime-speech-translator/internal/mt/sp"
)

// M2M100Config configures the M2M-100 backend (the canonical
// facebook/m2m100_418M model, CTranslate2 int8 export). Unlike
// SMaLL-100 (small100.go), the target-language tag goes on the
// decoder side as a single-piece prefix, and the source-language
// tag prefixes the encoder input.
type M2M100Config struct {
	ModelDir           string
	SentencePieceModel string

	BeamSize          int
	MaxDecodingLength int
	Threads           int
	ComputeType       ct2.ComputeType
}

// DefaultM2M100Config returns a Config tuned for low-latency CPU.
//
// Threads defaults to max(NumCPU/2, 2): with Threads=0 the CT2
// shim picks 1, which on m2m100-418M turns a 2-second translation
// into a 47-second one and routinely collapses the decoder into
// "and and and …" loops because the beam search runs out of steam.
//
// MaxDecodingLength capped at 128 — the loop-collapse pathology
// can still happen on adversarial inputs; bounding the output
// stops a runaway from wasting tens of seconds before we get a
// usable signal that something is wrong.
func DefaultM2M100Config(modelDir, spModel string) M2M100Config {
	t := runtime.NumCPU() / 2
	if t < 2 {
		t = 2
	}
	return M2M100Config{
		ModelDir:           modelDir,
		SentencePieceModel: spModel,
		BeamSize:           1,
		MaxDecodingLength:  128,
		Threads:            t,
		ComputeType:        ct2.ComputeInt8,
	}
}

// M2M100 is the original-scheme backend (vs SMaLL-100's distilled
// scheme). 418M parameters, 100 languages, ~500 MB int8 export.
type M2M100 struct {
	cfg M2M100Config
	tr  *ct2.Translator
	tok *sp.Processor
}

// NewM2M100 constructs an m2m100 engine.
func NewM2M100(cfg M2M100Config) (*M2M100, error) {
	if cfg.ModelDir == "" {
		return nil, fmt.Errorf("m2m100: empty model dir")
	}
	if cfg.SentencePieceModel == "" {
		return nil, fmt.Errorf("m2m100: empty sentencepiece model")
	}
	t, err := ct2.New(cfg.ModelDir, ct2.Options{
		ComputeType: cfg.ComputeType,
		Threads:     cfg.Threads,
	})
	if err != nil {
		return nil, fmt.Errorf("m2m100: load ct2: %w", err)
	}
	tok, err := sp.Load(cfg.SentencePieceModel)
	if err != nil {
		_ = t.Close()
		return nil, fmt.Errorf("m2m100: load sp: %w", err)
	}
	return &M2M100{cfg: cfg, tr: t, tok: tok}, nil
}

// Translate runs m2m100 over one source sentence.
//
//	encoder pieces  = ["__src__", piece_1, piece_2, …]
//	decoder prefix  = ["__tgt__"]
func (m *M2M100) Translate(src, srcLang, dstLang string) (string, error) {
	if !small100Supports(srcLang) {
		return "", fmt.Errorf("%w: src %q", ErrUnsupportedLang, srcLang)
	}
	if !small100Supports(dstLang) {
		return "", fmt.Errorf("%w: dst %q", ErrUnsupportedLang, dstLang)
	}
	srcTag := "__" + strings.ToLower(srcLang) + "__"
	dstTag := "__" + strings.ToLower(dstLang) + "__"

	pieces, err := m.tok.EncodePieces(strings.TrimSpace(src))
	if err != nil {
		return "", fmt.Errorf("m2m100: tokenize: %w", err)
	}

	sourcePieces := make([]string, 0, len(pieces)+1)
	sourcePieces = append(sourcePieces, srcTag)
	sourcePieces = append(sourcePieces, pieces...)

	outPieces, err := m.tr.Translate(sourcePieces, ct2.TranslateOptions{
		BeamSize:           m.cfg.BeamSize,
		MaxDecodingLength:  m.cfg.MaxDecodingLength,
		TargetPrefixPieces: []string{dstTag},
	})
	if err != nil {
		return "", fmt.Errorf("m2m100: translate: %w", err)
	}
	for len(outPieces) > 0 && outPieces[0] == dstTag {
		outPieces = outPieces[1:]
	}
	text, err := m.tok.DecodePieces(outPieces)
	if err != nil {
		return "", fmt.Errorf("m2m100: decode: %w", err)
	}
	return strings.TrimSpace(text), nil
}

// Close releases CT2 + SP native handles.
func (m *M2M100) Close() error {
	var firstErr error
	if err := m.tr.Close(); err != nil {
		firstErr = err
	}
	if err := m.tok.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

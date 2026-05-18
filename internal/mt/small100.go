//go:build mt

package mt

import (
	"fmt"
	"strings"

	"github.com/alex60217101990/realtime-speech-translator/internal/mt/ct2"
	"github.com/alex60217101990/realtime-speech-translator/internal/mt/sp"
)

// SMaLL100Config configures the SMaLL-100 backend (a distilled
// derivative of M2M-100, 100-language, 330 MB int8 footprint, runs at
// realtime rates on a modest CPU).
//
// The distillation re-trained the model with the target language token
// on the *encoder* side; there is no decoder target prefix. That is
// the only deviation from the original m2m100 tagging scheme.
type SMaLL100Config struct {
	ModelDir           string
	SentencePieceModel string

	BeamSize          int
	MaxDecodingLength int
	Threads           int
	ComputeType       ct2.ComputeType
}

// DefaultSMaLL100Config returns a Config tuned for low-latency CPU.
func DefaultSMaLL100Config(modelDir, spModel string) SMaLL100Config {
	return SMaLL100Config{
		ModelDir:           modelDir,
		SentencePieceModel: spModel,
		BeamSize:           1,
		MaxDecodingLength:  256,
		Threads:            0,
		ComputeType:        ct2.ComputeInt8,
	}
}

// SMaLL100 is a CTranslate2 backend for the SMaLL-100 model.
type SMaLL100 struct {
	cfg SMaLL100Config
	tr  *ct2.Translator
	tok *sp.Processor
}

// NewSMaLL100 constructs a SMaLL-100 engine. The model directory must
// be a CTranslate2 export; the sentencepiece file is typically
// "sentencepiece.bpe.model" alongside.
func NewSMaLL100(cfg SMaLL100Config) (*SMaLL100, error) {
	if cfg.ModelDir == "" {
		return nil, fmt.Errorf("small100: empty model dir")
	}
	if cfg.SentencePieceModel == "" {
		return nil, fmt.Errorf("small100: empty sentencepiece model")
	}
	t, err := ct2.New(cfg.ModelDir, ct2.Options{
		ComputeType: cfg.ComputeType,
		Threads:     cfg.Threads,
	})
	if err != nil {
		return nil, fmt.Errorf("small100: load ct2: %w", err)
	}
	tok, err := sp.Load(cfg.SentencePieceModel)
	if err != nil {
		_ = t.Close()
		return nil, fmt.Errorf("small100: load sp: %w", err)
	}
	return &SMaLL100{cfg: cfg, tr: t, tok: tok}, nil
}

// Translate runs SMaLL-100 over a single source sentence.
func (m *SMaLL100) Translate(src, srcLang, dstLang string) (string, error) {
	if !small100Supports(srcLang) {
		return "", fmt.Errorf("%w: src %q", ErrUnsupportedLang, srcLang)
	}
	if !small100Supports(dstLang) {
		return "", fmt.Errorf("%w: dst %q", ErrUnsupportedLang, dstLang)
	}
	dstTag := "__" + strings.ToLower(dstLang) + "__"

	pieces, err := m.tok.EncodePieces(strings.TrimSpace(src))
	if err != nil {
		return "", fmt.Errorf("small100: tokenize: %w", err)
	}

	// SMaLL-100 tagging: encoder = ["__tgt__", pieces…], decoder = nil.
	sourcePieces := make([]string, 0, len(pieces)+1)
	sourcePieces = append(sourcePieces, dstTag)
	sourcePieces = append(sourcePieces, pieces...)

	outPieces, err := m.tr.Translate(sourcePieces, ct2.TranslateOptions{
		BeamSize:          m.cfg.BeamSize,
		MaxDecodingLength: m.cfg.MaxDecodingLength,
	})
	if err != nil {
		return "", fmt.Errorf("small100: translate: %w", err)
	}
	// Strip the language tag if the decoder echoed it back. SMaLL-100
	// usually doesn't, but some quantisations do; harmless either way.
	for len(outPieces) > 0 && outPieces[0] == dstTag {
		outPieces = outPieces[1:]
	}
	text, err := m.tok.DecodePieces(outPieces)
	if err != nil {
		return "", fmt.Errorf("small100: decode: %w", err)
	}
	return strings.TrimSpace(text), nil
}

func (m *SMaLL100) Close() error {
	var firstErr error
	if err := m.tr.Close(); err != nil {
		firstErr = err
	}
	if err := m.tok.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// small100Supports returns true if the ISO-639-1 code is in
// SMaLL-100's 100-language vocabulary (inherited from M2M-100).
func small100Supports(iso string) bool {
	_, ok := small100Langs[strings.ToLower(iso)]
	return ok
}

var small100Langs = map[string]struct{}{
	"af": {}, "am": {}, "ar": {}, "ast": {}, "az": {}, "ba": {}, "be": {},
	"bg": {}, "bn": {}, "br": {}, "bs": {}, "ca": {}, "ceb": {}, "cs": {},
	"cy": {}, "da": {}, "de": {}, "el": {}, "en": {}, "es": {}, "et": {},
	"fa": {}, "ff": {}, "fi": {}, "fr": {}, "fy": {}, "ga": {}, "gd": {},
	"gl": {}, "gu": {}, "ha": {}, "he": {}, "hi": {}, "hr": {}, "ht": {},
	"hu": {}, "hy": {}, "id": {}, "ig": {}, "ilo": {}, "is": {}, "it": {},
	"ja": {}, "jv": {}, "ka": {}, "kk": {}, "km": {}, "kn": {}, "ko": {},
	"lb": {}, "lg": {}, "ln": {}, "lo": {}, "lt": {}, "lv": {}, "mg": {},
	"mk": {}, "ml": {}, "mn": {}, "mr": {}, "ms": {}, "my": {}, "ne": {},
	"nl": {}, "no": {}, "ns": {}, "oc": {}, "or": {}, "pa": {}, "pl": {},
	"ps": {}, "pt": {}, "ro": {}, "ru": {}, "sd": {}, "si": {}, "sk": {},
	"sl": {}, "so": {}, "sq": {}, "sr": {}, "ss": {}, "su": {}, "sv": {},
	"sw": {}, "ta": {}, "th": {}, "tl": {}, "tn": {}, "tr": {}, "uk": {},
	"ur": {}, "uz": {}, "vi": {}, "wo": {}, "xh": {}, "yi": {}, "yo": {},
	"zh": {}, "zu": {},
}

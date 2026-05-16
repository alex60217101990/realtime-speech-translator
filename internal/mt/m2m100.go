package mt

import (
	"fmt"
	"strings"

	"github.com/alex60217101990/realtime-speech-translator/internal/mt/ct2"
	"github.com/alex60217101990/realtime-speech-translator/internal/mt/sp"
)

// M2M100Config configures the M2M-100 family backend (m2m100, SMaLL-100,
// and any future derivative that shares the same SentencePiece + CT2
// runtime). TargetPrefixOnSource switches between the two known
// tagging schemes.
type M2M100Config struct {
	ModelDir           string
	SentencePieceModel string

	BeamSize          int
	MaxDecodingLength int
	Threads           int
	ComputeType       ct2.ComputeType

	// TargetPrefixOnSource selects the tagging scheme:
	//
	//   false (default, m2m100):
	//     source pieces  = ["__src_lang__", "▁hello", …]
	//     decoder prefix = ["__tgt_lang__"]
	//
	//   true (SMaLL-100):
	//     source pieces  = ["__tgt_lang__", "▁hello", …]
	//     decoder prefix = nil
	//
	// SMaLL-100's distillation re-trained the model with the target
	// language token on the encoder side, so the m2m100 scheme would
	// produce garbage. Set this flag when ModelDir points at a
	// SMaLL-100 checkpoint.
	TargetPrefixOnSource bool
}

// DefaultM2M100Config returns a Config tuned for low-latency CPU.
func DefaultM2M100Config(modelDir, spModel string) M2M100Config {
	return M2M100Config{
		ModelDir:           modelDir,
		SentencePieceModel: spModel,
		BeamSize:           1,
		MaxDecodingLength:  256,
		Threads:            0,
		ComputeType:        ct2.ComputeInt8,
	}
}

// DefaultSMaLL100Config returns a Config preconfigured for SMaLL-100.
// Same CT2 + SentencePiece pipeline as m2m100, only the tagging flag
// differs.
func DefaultSMaLL100Config(modelDir, spModel string) M2M100Config {
	c := DefaultM2M100Config(modelDir, spModel)
	c.TargetPrefixOnSource = true
	return c
}

// M2M100 is a CTranslate2 backend for Facebook's M2M-100 multilingual
// model. It is ~7× smaller than MADLAD-3B and ~3-5× faster on CPU,
// making it a better default for realtime translation while still
// covering 100 languages with a single model.
//
// Tag scheme differs from MADLAD:
//
//   - Source pieces are prefixed with `__<src_lang>__` (e.g. `__ru__`).
//   - Decoder gets a single-piece target prefix `__<dst_lang>__` so
//     that the model emits in the right language.
type M2M100 struct {
	cfg M2M100Config
	tr  *ct2.Translator
	tok *sp.Processor
}

// NewM2M100 constructs an M2M-100 engine. The model directory must be
// a CTranslate2 export; the sentencepiece file is typically
// "sentencepiece.bpe.model" alongside.
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

// Translate runs M2M-100 (or SMaLL-100) over a single source sentence.
func (m *M2M100) Translate(src, srcLang, dstLang string) (string, error) {
	if !m2m100Supports(srcLang) {
		return "", fmt.Errorf("%w: src %q", ErrUnsupportedLang, srcLang)
	}
	if !m2m100Supports(dstLang) {
		return "", fmt.Errorf("%w: dst %q", ErrUnsupportedLang, dstLang)
	}
	srcTag := "__" + strings.ToLower(srcLang) + "__"
	dstTag := "__" + strings.ToLower(dstLang) + "__"

	pieces, err := m.tok.EncodePieces(strings.TrimSpace(src))
	if err != nil {
		return "", fmt.Errorf("m2m100: tokenize: %w", err)
	}

	// Tagging scheme:
	//
	//   m2m100   : encoder = ["__src__", pieces…], decoder = ["__tgt__"]
	//   SMaLL-100: encoder = ["__tgt__", pieces…], decoder = nil
	//
	// SMaLL-100 was distilled with the target language token on the
	// encoder side; using the m2m100 scheme on it produces garbage.
	sourcePieces := make([]string, 0, len(pieces)+1)
	var decoderPrefix []string
	if m.cfg.TargetPrefixOnSource {
		sourcePieces = append(sourcePieces, dstTag)
	} else {
		sourcePieces = append(sourcePieces, srcTag)
		decoderPrefix = []string{dstTag}
	}
	sourcePieces = append(sourcePieces, pieces...)

	outPieces, err := m.tr.Translate(sourcePieces, ct2.TranslateOptions{
		BeamSize:           m.cfg.BeamSize,
		MaxDecodingLength:  m.cfg.MaxDecodingLength,
		TargetPrefixPieces: decoderPrefix,
	})
	if err != nil {
		return "", fmt.Errorf("m2m100: translate: %w", err)
	}
	// Strip the language tag if the decoder echoed it back. SMaLL-100
	// doesn't prepend it, but a few m2m100 quantisations do; harmless
	// to check either way.
	for len(outPieces) > 0 && outPieces[0] == dstTag {
		outPieces = outPieces[1:]
	}
	text, err := m.tok.DecodePieces(outPieces)
	if err != nil {
		return "", fmt.Errorf("m2m100: decode: %w", err)
	}
	return strings.TrimSpace(text), nil
}

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

// m2m100Supports returns true if the ISO-639-1 code is in M2M-100's
// 100-language vocabulary. Listed conservatively; extend if a user
// reports a missing pair.
func m2m100Supports(iso string) bool {
	_, ok := m2m100Langs[strings.ToLower(iso)]
	return ok
}

var m2m100Langs = map[string]struct{}{
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

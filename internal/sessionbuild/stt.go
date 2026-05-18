package sessionbuild

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/alex60217101990/realtime-speech-translator/internal/config"
	"github.com/alex60217101990/realtime-speech-translator/internal/stt"
)

// buildSTTBackend picks the right stt.Backend by inspecting which
// model files are on disk:
//
//   - encoder.onnx     → streaming Zipformer transducer.
//   - model.onnx       → streaming T-one CTC.
//   - model.int8.onnx  → offline NeMo CTC (GigaAM family).
func buildSTTBackend(cfg config.Settings, sttDir, tokensPath, vadPath string) (stt.Backend, string, error) {
	switch {
	case fileExists(filepath.Join(sttDir, "encoder.onnx")):
		for _, leaf := range []string{"decoder.onnx", "joiner.onnx"} {
			if !fileExists(filepath.Join(sttDir, leaf)) {
				return nil, "", fmt.Errorf("%w: stt/%s/%s", ErrModelsMissing, cfg.STTModel, leaf)
			}
		}
		c := stt.DefaultConfig()
		c.Kind = stt.ModelTransducer
		c.Encoder = filepath.Join(sttDir, "encoder.onnx")
		c.Decoder = filepath.Join(sttDir, "decoder.onnx")
		c.Joiner = filepath.Join(sttDir, "joiner.onnx")
		c.Tokens = tokensPath
		c.VADModel = vadPath
		c.NumThreads = cfg.Threads
		if cfg.VADThreshold > 0 {
			c.VADThreshold = cfg.VADThreshold
		}
		e, err := stt.New(c)
		if err != nil {
			return nil, "", err
		}
		return e, "transducer (streaming Zipformer)", nil

	case fileExists(filepath.Join(sttDir, "model.onnx")):
		c := stt.DefaultToneCtcConfig()
		c.Kind = stt.ModelToneCtc
		c.ToneCtcModel = filepath.Join(sttDir, "model.onnx")
		c.Tokens = tokensPath
		c.VADModel = vadPath
		c.NumThreads = cfg.Threads
		if cfg.VADThreshold > 0 {
			c.VADThreshold = cfg.VADThreshold
		}
		e, err := stt.New(c)
		if err != nil {
			return nil, "", err
		}
		return e, "tone_ctc (T-one streaming)", nil

	case fileExists(filepath.Join(sttDir, "model.int8.onnx")):
		c := stt.DefaultVadNemoConfig()
		c.NemoCTCModel = filepath.Join(sttDir, "model.int8.onnx")
		c.Tokens = tokensPath
		c.VADModel = vadPath
		c.NumThreads = cfg.Threads
		if cfg.VADThreshold > 0 {
			c.VADThreshold = cfg.VADThreshold
		}
		e, err := stt.NewVadNemo(c)
		if err != nil {
			return nil, "", err
		}
		return e, "vad_nemo (offline NeMo CTC)", nil
	}
	return nil, "", fmt.Errorf("%w: stt/%s/(encoder|model|model.int8).onnx", ErrModelsMissing, cfg.STTModel)
}

// ResolveSTTModel returns the concrete STT directory name to load.
// When cfg.STTModel is anything other than "" or "auto" the user's
// explicit choice wins. Otherwise picks the best installed model
// for cfg.SourceLang from the priority table below.
func ResolveSTTModel(cfg config.Settings) (name, reason string) {
	if cfg.STTModel != "" && cfg.STTModel != "auto" {
		return cfg.STTModel, ""
	}
	installed := map[string]bool{}
	for _, n := range ListInstalled("stt") {
		installed[n] = true
	}
	if len(installed) == 0 {
		return "", ""
	}

	var order []string
	switch strings.ToLower(cfg.SourceLang) {
	case "ru":
		order = []string{
			"nemo-ctc-punct-giga-am-v3-russian",
			"nemo-ctc-giga-am-v3-russian",
			"nemo-ctc-giga-am-v2-russian",
			"streaming-t-one-russian",
		}
	case "en":
		order = []string{"zipformer-streaming-en"}
	default:
		order = []string{
			"nemo-ctc-punct-giga-am-v3-russian",
			"zipformer-streaming-en",
			"nemo-ctc-giga-am-v3-russian",
			"nemo-ctc-giga-am-v2-russian",
			"streaming-t-one-russian",
		}
	}
	src := cfg.SourceLang
	if src == "" {
		src = "auto"
	}
	for _, candidate := range order {
		if installed[candidate] {
			return candidate, fmt.Sprintf("source=%s, best installed match", src)
		}
	}
	for n := range installed {
		return n, "no preferred match; fell back to first installed"
	}
	return "", ""
}

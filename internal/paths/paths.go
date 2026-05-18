// Package paths centralises filesystem locations the application reads
// and writes. Anything that touches the user's data directory should go
// through here so behaviour stays consistent across platforms.
//
// Layout (XDG on Linux, Library/Application Support on macOS, AppData
// on Windows):
//
//	data/
//	├── models/
//	│   ├── stt/<name>/{encoder.onnx, decoder.onnx, joiner.onnx, tokens.txt}
//	│   ├── vad/silero_vad.onnx
//	│   ├── mt/<name>/{model.bin, config.json, sentencepiece.bpe.model}
//	│   └── tts/<name>/{model.onnx, tokens.txt, espeak-ng-data/}
//	└── config.yaml
package paths

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

const appDir = "realtime-speech-translator"

// Data returns the root data directory for the application.
func Data() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", appDir), nil
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(base, appDir), nil
	default:
		base := os.Getenv("XDG_DATA_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, ".local", "share")
		}
		return filepath.Join(base, appDir), nil
	}
}

// Config returns the configuration root. On Linux this is
// $XDG_CONFIG_HOME (default ~/.config); other OSes use the same path as
// Data() so a single tree is portable when users sync between machines.
func Config() (string, error) {
	if runtime.GOOS == "linux" {
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, ".config")
		}
		return filepath.Join(base, appDir), nil
	}
	return Data()
}

// Models returns the models root, ensuring it exists.
func Models() (string, error) {
	d, err := Data()
	if err != nil {
		return "", err
	}
	p := filepath.Join(d, "models")
	if err := os.MkdirAll(p, 0o755); err != nil {
		return "", err
	}
	return p, nil
}

// STTDir returns the local directory for a named streaming ASR model
// (sherpa-onnx online transducer/paraformer export). Created on demand.
func STTDir(name string) (string, error) {
	if name == "" {
		return "", errors.New("paths: empty STT name")
	}
	m, err := Models()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(m, "stt", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// VADModel returns the path of the Silero VAD onnx file.
func VADModel() (string, error) {
	m, err := Models()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(m, "vad")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "silero_vad.onnx"), nil
}

// MTDir returns the local directory for a named MT model
// (CTranslate2 export). Created on demand.
func MTDir(name string) (string, error) {
	if name == "" {
		return "", errors.New("paths: empty MT name")
	}
	m, err := Models()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(m, "mt", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// OPUSMTPair returns the directory for a single OPUS-MT language pair
// (e.g. "ru-en"). It lives under mt/opusmt/<pair>/ so the OPUS-MT
// loader can discover every downloaded pair by listing one parent.
func OPUSMTPair(pair string) (string, error) {
	if pair == "" {
		return "", errors.New("paths: empty OPUS-MT pair")
	}
	m, err := Models()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(m, "mt", "opusmt", pair)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// OPUSMTRoot returns the root for OPUS-MT pair downloads, used by the
// loader to enumerate available pairs.
func OPUSMTRoot() (string, error) {
	m, err := Models()
	if err != nil {
		return "", err
	}
	return filepath.Join(m, "mt", "opusmt"), nil
}

// TTSDir returns the local directory for a named TTS voice
// (sherpa-onnx Piper export: model.onnx + tokens.txt + espeak-ng-data/).
func TTSDir(name string) (string, error) {
	if name == "" {
		return "", errors.New("paths: empty TTS name")
	}
	m, err := Models()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(m, "tts", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// TranslationMemory returns the path of the persistent translation
// memory JSON file. Parent dir is the application data root.
func TranslationMemory() (string, error) {
	d, err := Data()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "translation_memory.json"), nil
}

// Exists is a small convenience that hides the os.Stat dance.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// FileSize returns the size in bytes, or zero if the file does not
// exist or stat fails.
func FileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return 0
	}
	return info.Size()
}

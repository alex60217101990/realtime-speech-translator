// Package wsapi wraps Microsoft Speech API (SAPI) as a TTS backend
// on Windows by spawning PowerShell with a minimal inline script that
// drives System.Speech.Synthesis.SpeechSynthesizer.
//
// SAPI ships with every Windows install since XP; English voices
// (David, Zira, Mark) work out of the box. Russian voices (Pavel,
// Irina) require the user to install the Russian language pack
// through Settings → Time & Language → Language. The same path adds
// voices for any other locale.
package wsapi

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// SampleRate matches the other TTS backends so the playback ring
// stays on one fixed rate. SAPI returns 22050 Hz mono with the
// SetOutputToWaveFile path we use here.
const SampleRate = 22050

// Voice maps an ISO-639-1 language to a SAPI voice name (one of the
// strings returned by SpeechSynthesizer.GetInstalledVoices()).
type Voice struct {
	Lang string
	Name string // e.g. "Microsoft Zira Desktop", "Microsoft Pavel Desktop"
}

// Engine renders text into 22050 Hz mono int16 PCM via SAPI.
type Engine struct {
	mu      sync.RWMutex
	voices  map[string]Voice
	psBin   string
	timeout time.Duration
}

// New constructs an Engine. Returns an error on non-Windows hosts or
// when powershell.exe is missing.
func New() (*Engine, error) {
	if runtime.GOOS != "windows" {
		return nil, errors.New("wsapi: only supported on Windows")
	}
	bin, err := exec.LookPath("powershell")
	if err != nil {
		bin, err = exec.LookPath("powershell.exe")
		if err != nil {
			return nil, fmt.Errorf("wsapi: powershell not found: %w", err)
		}
	}
	e := &Engine{
		psBin:   bin,
		voices:  make(map[string]Voice, 8),
		timeout: 15 * time.Second,
	}
	for _, v := range defaultVoices {
		e.voices[v.Lang] = v
	}
	return e, nil
}

// defaultVoices use partial names that SAPI matches via substring.
// Built-in EN voices ship everywhere; non-EN entries require the
// matching Windows Language Pack to be installed first.
var defaultVoices = []Voice{
	{Lang: "en", Name: "Zira"},
	{Lang: "ru", Name: "Irina"},
	{Lang: "uk", Name: "Daria"},
	{Lang: "es", Name: "Sabina"},
	{Lang: "de", Name: "Hedda"},
	{Lang: "fr", Name: "Hortense"},
	{Lang: "it", Name: "Elsa"},
	{Lang: "pt", Name: "Maria"},
	{Lang: "pl", Name: "Paulina"},
	{Lang: "nl", Name: "Frank"},
	{Lang: "tr", Name: "Tolga"},
	{Lang: "ja", Name: "Haruka"},
	{Lang: "zh", Name: "Huihui"},
}

// SetVoice overrides the voice used for a language.
func (e *Engine) SetVoice(v Voice) error {
	if v.Lang == "" || v.Name == "" {
		return errors.New("wsapi: voice requires Lang and Name")
	}
	e.mu.Lock()
	e.voices[strings.ToLower(v.Lang)] = v
	e.mu.Unlock()
	return nil
}

// HasVoice reports whether a voice is registered for the language.
func (e *Engine) HasVoice(lang string) bool {
	e.mu.RLock()
	_, ok := e.voices[strings.ToLower(lang)]
	e.mu.RUnlock()
	return ok
}

// Synthesize runs SAPI via PowerShell and returns 22050 Hz mono
// int16 PCM. Matches stt.TTSEngine.Synthesize.
func (e *Engine) Synthesize(ctx context.Context, text, lang string) ([]int16, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("wsapi: empty text")
	}
	e.mu.RLock()
	voice, ok := e.voices[strings.ToLower(lang)]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("wsapi: no voice registered for %q", lang)
	}

	tmp, err := os.CreateTemp("", "rst-sapi-*.wav")
	if err != nil {
		return nil, fmt.Errorf("wsapi: tempfile: %w", err)
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(path)

	// Inline PowerShell that loads System.Speech, picks a voice by
	// substring match, renders the stdin payload to a WAV file at
	// 22050 Hz mono int16, and exits. The voice/text strings are
	// passed via environment variables so we never have to quote-
	// escape arbitrary user input into the PowerShell command line.
	script := `
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Speech
$synth = New-Object System.Speech.Synthesis.SpeechSynthesizer
$voiceName = $env:RST_VOICE
foreach ($v in $synth.GetInstalledVoices()) {
    if ($v.VoiceInfo.Name -like "*${voiceName}*") {
        $synth.SelectVoice($v.VoiceInfo.Name)
        break
    }
}
$fmt = New-Object System.Speech.AudioFormat.SpeechAudioFormatInfo 22050, ([System.Speech.AudioFormat.AudioBitsPerSample]::Sixteen), ([System.Speech.AudioFormat.AudioChannel]::Mono)
$synth.SetOutputToWaveFile($env:RST_OUT, $fmt)
$synth.Speak($env:RST_TEXT)
$synth.Dispose()
`
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, e.psBin,
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-Command", script,
	)
	cmd.Env = append(os.Environ(),
		"RST_VOICE="+voice.Name,
		"RST_TEXT="+text,
		"RST_OUT="+path,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("wsapi: powershell %w (stderr=%q)", err, stderr.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("wsapi: read output: %w", err)
	}
	return parseWavPCM16(raw)
}

// Close is a no-op — each Synthesize spawns a fresh PowerShell.
func (e *Engine) Close() error { return nil }

// parseWavPCM16 mirrors the helper in internal/tts/sayos so all three
// platform backends produce identical PCM payloads.
func parseWavPCM16(b []byte) ([]int16, error) {
	if len(b) < 44 {
		return nil, errors.New("wsapi: WAVE too small")
	}
	if string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, errors.New("wsapi: not a RIFF/WAVE file")
	}
	off := 12
	for off+8 <= len(b) {
		tag := string(b[off : off+4])
		size := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		off += 8
		if tag == "data" {
			end := off + size
			if end > len(b) {
				end = len(b)
			}
			payload := b[off:end]
			if len(payload)%2 != 0 {
				payload = payload[:len(payload)-1]
			}
			n := len(payload) / 2
			if n == 0 {
				return nil, nil
			}
			return unsafe.Slice((*int16)(unsafe.Pointer(&payload[0])), n), nil
		}
		off += size
		if size%2 == 1 {
			off++
		}
	}
	return nil, errors.New("wsapi: no data chunk in WAVE output")
}

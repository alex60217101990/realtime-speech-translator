package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alex60217101990/realtime-speech-translator/internal/config"
	"github.com/alex60217101990/realtime-speech-translator/internal/models"
	"github.com/alex60217101990/realtime-speech-translator/internal/paths"
	"github.com/alex60217101990/realtime-speech-translator/internal/uiebt"
)

// settingsSnapshotFromCfg projects the on-disk config.Settings into
// the UI-facing SettingsSnapshot, filling the STT/TTS dropdown
// options from whatever is actually installed on disk + the
// catalog.
func settingsSnapshotFromCfg(cfg config.Settings) uiebt.SettingsSnapshot {
	return uiebt.SettingsSnapshot{
		SourceLang:   cfg.SourceLang,
		TargetLang:   cfg.TargetLang,
		STTModel:     cfg.STTModel,
		MTBackend:    cfg.MTBackend,
		TTSVoice:     cfg.TTSVoice,
		TTSEnabled:   cfg.TTSEnabled,
		VADThreshold: cfg.VADThreshold,
		Threads:      cfg.Threads,
		OutputDevice: cfg.OutputDevice,
		STTOptions:   discoverModelOptions(models.KindSTT, "auto"),
		TTSOptions:   discoverModelOptions(models.KindTTS, ""),
	}
}

// discoverModelOptions returns the union of:
//   - subdirectories present under <data>/models/<kind>/
//   - catalog entries of that Kind (so a not-yet-installed entry
//     can still be picked, letting the next session-build prompt
//     an install)
//
// extra is prepended if non-empty — used to surface "auto" for STT.
func discoverModelOptions(kind models.Kind, extra string) []string {
	seen := map[string]struct{}{}
	var out []string
	if extra != "" {
		out = append(out, extra)
		seen[extra] = struct{}{}
	}

	// On-disk dirs first — these are guaranteed loadable.
	mroot, err := paths.Models()
	var root string
	if err == nil {
		switch kind {
		case models.KindSTT:
			root = filepath.Join(mroot, "stt")
		case models.KindTTS:
			root = filepath.Join(mroot, "tts")
		}
	}
	if root != "" {
		entries, _ := os.ReadDir(root)
		var dirs []string
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, e.Name())
			}
		}
		sort.Strings(dirs)
		for _, d := range dirs {
			if _, ok := seen[d]; ok {
				continue
			}
			out = append(out, d)
			seen[d] = struct{}{}
		}
	}

	// Catalog entries as fallback so unsettled installs are still
	// pickable.
	for _, e := range models.DefaultCatalog() {
		if e.Kind != kind {
			continue
		}
		if _, ok := seen[e.Name]; ok {
			continue
		}
		out = append(out, e.Name)
		seen[e.Name] = struct{}{}
	}
	return out
}

// buildSettingsSaver returns a SettingsSaver closure that writes
// the edited config back to disk + signals the session loop to
// rebuild so the change takes effect without a process restart.
func buildSettingsSaver(initial config.Settings, sess *sessionHandle) uiebt.SettingsSaver {
	return func(snap uiebt.SettingsSnapshot) error {
		updated := initial
		updated.SourceLang = strings.TrimSpace(snap.SourceLang)
		updated.TargetLang = strings.TrimSpace(snap.TargetLang)
		updated.STTModel = strings.TrimSpace(snap.STTModel)
		updated.MTBackend = strings.TrimSpace(snap.MTBackend)
		updated.TTSVoice = strings.TrimSpace(snap.TTSVoice)
		updated.TTSEnabled = snap.TTSEnabled
		updated.VADThreshold = snap.VADThreshold
		updated.Threads = snap.Threads
		updated.OutputDevice = snap.OutputDevice
		if err := config.Save(updated); err != nil {
			return err
		}
		p, _ := config.Path()
		slog.Info("settings saved", "path", p,
			"src", updated.SourceLang, "tgt", updated.TargetLang,
			"mt", updated.MTBackend, "tts", updated.TTSVoice,
			"stt", updated.STTModel, "tts_enabled", updated.TTSEnabled)
		if sess != nil {
			sess.Reload()
		}
		return nil
	}
}


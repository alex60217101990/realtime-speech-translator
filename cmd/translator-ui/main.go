// Command translator-ui is the Ebiten-based native UI for the
// realtime speech translator.
//
// Built on top of github.com/hajimehoshi/ebiten/v2 + Kage shaders,
// it ships as a single binary with ~30–50 MB RAM at steady state
// (versus ~150 MB for the original Wails / WebView design that was
// considered and dropped). The engine layers (internal/{stt,tts,
// mt,audio,app}) stay untouched; this binary imports them through
// internal/uiebt and the internal/uihost IPC contract.
//
// Current state: Stage 1 — window boot, gradient background and a
// placeholder card. Subsequent commits add the header, transcript
// + translation panes, sphere shader, Models tab and Settings tab,
// in that order.
//
// The Fyne shell at cmd/translator stays as the fallback target
// until parity with this UI is reached.
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/alex60217101990/realtime-speech-translator/internal/logging"
	"github.com/alex60217101990/realtime-speech-translator/internal/uiebt"
)

func main() {
	closeLog, err := logging.Init(logging.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "log init:", err)
	} else {
		defer closeLog()
	}
	slog.Info("translator-ui starting", "stack", "ebiten")

	if err := uiebt.Run(); err != nil {
		slog.Error("ui exited with error", "err", err)
		os.Exit(1)
	}
}

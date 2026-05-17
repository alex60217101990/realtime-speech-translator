package ui

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/alex60217101990/realtime-speech-translator/internal/models/downloader"
	"github.com/alex60217101990/realtime-speech-translator/internal/models/paths"
)

// piperRelease is the version of the upstream rhasspy/piper release we
// install. Pinned so the install flow is reproducible and we never
// silently pick up a breaking release. Update with intent.
const piperRelease = "2023.11.14-2"

// piperReleaseURL returns the OS/arch-specific tarball URL or empty
// when the host has no supported pre-built release (in which case the
// row goes manual-install).
func piperReleaseURL() string {
	base := "https://github.com/rhasspy/piper/releases/download/" + piperRelease
	switch runtime.GOOS {
	case "darwin":
		switch runtime.GOARCH {
		case "arm64":
			return base + "/piper_macos_aarch64.tar.gz"
		case "amd64":
			return base + "/piper_macos_x64.tar.gz"
		}
	case "linux":
		switch runtime.GOARCH {
		case "amd64":
			return base + "/piper_linux_x86_64.tar.gz"
		case "arm64":
			return base + "/piper_linux_aarch64.tar.gz"
		case "arm":
			return base + "/piper_linux_armv7l.tar.gz"
		}
	}
	// Windows release is a .zip — extractor only handles tar.gz today,
	// so we fall back to manual install with a README pointer.
	return ""
}

// piperEngineRow constructs the "Piper engine" row that auto-installs
// the upstream piper binary + bundled dylibs from the GitHub release
// matching the host OS/arch. The row sits at the top of the Models
// tab list above whisper/mt/tts rows.
func piperEngineRow(w fyne.Window, dl *downloader.Downloader, onInstalled func()) fyne.CanvasObject {
	url := piperReleaseURL()
	binPath, _ := paths.PiperBinary()
	statusLbl := widget.NewLabel(piperStatusText(binPath, url))
	statusLbl.Truncation = fyne.TextTruncateEllipsis
	progress := widget.NewProgressBar()
	progress.Hide()
	var btn *widget.Button
	btn = widget.NewButton(piperActionText(binPath, url), func() {
		if url == "" {
			return // manual install only
		}
		btn.Disable()
		progress.Show()
		go func() {
			err := installPiperEngine(context.Background(), dl, url, progress, statusLbl)
			fyne.Do(func() {
				if err != nil {
					statusLbl.SetText("error: " + truncateText(err.Error(), 80))
				} else {
					statusLbl.SetText(piperStatusText(binPath, url))
					btn.SetText(piperActionText(binPath, url))
				}
				progress.Hide()
				btn.Enable()
			})
			if err == nil && onInstalled != nil {
				onInstalled()
			}
		}()
	})
	if url == "" {
		btn.Disable()
	}
	label := widget.NewLabel("Piper engine  [REALTIME ★]")
	return container.NewBorder(
		nil, nil,
		label,
		btn,
		container.NewVBox(statusLbl, progress),
	)
}

func piperStatusText(binPath, url string) string {
	if paths.Exists(binPath) {
		return fmt.Sprintf("installed (%s) at %s", piperRelease, filepath.Dir(binPath))
	}
	if url == "" {
		return "manual install required — see README (no automatic release for this OS/arch)"
	}
	return fmt.Sprintf("missing — click Install to fetch %s", piperRelease)
}

func piperActionText(binPath, url string) string {
	if paths.Exists(binPath) {
		return "Re-install"
	}
	if url == "" {
		return "Manual install"
	}
	return "Install"
}

// installPiperEngine downloads the OS/arch-specific Piper tarball,
// extracts it into paths.PiperRoot(), strips the macOS quarantine
// extended attribute so Gatekeeper does not block the unsigned
// binary, and verifies the resulting binary is executable.
func installPiperEngine(ctx context.Context, dl *downloader.Downloader, url string, bar *widget.ProgressBar, status *widget.Label) error {
	root, err := paths.PiperRoot()
	if err != nil {
		return fmt.Errorf("paths: %w", err)
	}
	// HEAD-check the URL so we surface a clean error before the GET
	// pump starts and overwrites status with a "downloading" message.
	head, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err == nil {
		if r, err := http.DefaultClient.Do(head); err == nil {
			_ = r.Body.Close()
			if r.StatusCode >= 400 {
				return fmt.Errorf("release URL returned HTTP %d — pinned piper version may have been retracted upstream", r.StatusCode)
			}
		}
	}
	tmp := filepath.Join(root, "_piper.tar.gz")
	ch, err := dl.Fetch(ctx, url, tmp, "")
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	for p := range ch {
		// Reuse the existing progress renderer used by model rows.
		updatePiperProgress(bar, status, p)
	}
	if !paths.Exists(tmp) || paths.FileSize(tmp) == 0 {
		return fmt.Errorf("download failed — no archive at %s (check stderr for HTTP errors)", tmp)
	}
	fyne.Do(func() { status.SetText("unpacking…") })
	if err := downloader.ExtractTarGz(tmp, root); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("unpack: %w", err)
	}
	_ = os.Remove(tmp)
	bin := filepath.Join(root, "piper")
	if !paths.Exists(bin) {
		return fmt.Errorf("unpack finished but %s is missing", bin)
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		slog.Warn("piper: chmod failed", "err", err, "path", bin)
	}
	// Strip macOS Gatekeeper quarantine flag so the unsigned binary
	// can be exec'd without the user having to right-click → Open.
	// Best-effort; ignore failure (e.g. on Linux xattr binary missing).
	if runtime.GOOS == "darwin" {
		if err := exec.CommandContext(ctx, "xattr", "-dr", "com.apple.quarantine", root).Run(); err != nil {
			slog.Debug("piper: xattr quarantine strip failed (non-fatal)", "err", err)
		}
	}
	slog.Info("piper engine installed", "release", piperRelease, "root", root, "binary", bin)
	return nil
}

// updatePiperProgress is the engine-row twin of the per-row download
// updater in models_screen.go. Kept separate to avoid coupling
// engine_row.go to private helpers it does not otherwise need.
func updatePiperProgress(bar *widget.ProgressBar, status *widget.Label, p downloader.Progress) {
	fyne.Do(func() {
		if p.Total > 0 {
			bar.SetValue(float64(p.Bytes) / float64(p.Total))
		}
		mb := p.Bytes >> 20
		if p.Total > 0 {
			status.SetText(fmt.Sprintf("downloading: %d / %d MB", mb, p.Total>>20))
		} else {
			status.SetText(fmt.Sprintf("downloading: %d MB", mb))
		}
	})
}

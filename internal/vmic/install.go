package vmic

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
)

// InstallGuide bundles everything the UI needs to walk a user through
// installing a virtual audio driver. The Steps slice contains plain
// strings the UI renders as a numbered list; URL is the canonical
// download/documentation link.
type InstallGuide struct {
	OS    string
	Title string
	URL   string
	Steps []string
	// AutoInstallCmd, when non-empty, is the suggested command. On
	// Linux we can run it ourselves (`pactl load-module ...`); on
	// macOS / Windows we surface it for copy-paste only.
	AutoInstallCmd string
}

// GuideFor returns the install guide for the current OS.
func GuideFor() InstallGuide {
	return guideFor(runtime.GOOS)
}

func guideFor(goos string) InstallGuide {
	switch goos {
	case "darwin":
		return InstallGuide{
			OS:    "macOS",
			Title: "Install BlackHole 2ch",
			URL:   "https://existential.audio/blackhole/",
			Steps: []string{
				"Run `brew install --cask blackhole-2ch` in Terminal, OR",
				"Download the .pkg installer from the URL below and run it",
				"After install, return to this window and press Re-check",
			},
			AutoInstallCmd: "brew install --cask blackhole-2ch",
		}
	case "linux":
		return InstallGuide{
			OS:    "Linux",
			Title: "Create a PulseAudio null sink",
			URL:   "https://wiki.archlinux.org/title/PulseAudio/Examples#Creating_a_null_sink",
			Steps: []string{
				"We can create the sink automatically — press Auto-install",
				"Or run the command below manually",
				"In your communication app, pick `Monitor of rstranslator_out` as the microphone input",
			},
			AutoInstallCmd: `pactl load-module module-null-sink sink_name=rstranslator_out sink_properties=device.description="RST_Translator"`,
		}
	case "windows":
		return InstallGuide{
			OS:    "Windows",
			Title: "Install VB-CABLE",
			URL:   "https://vb-audio.com/Cable/",
			Steps: []string{
				"Download the .zip archive from the URL below",
				"Extract it and run VBCABLE_Setup_x64.exe as Administrator",
				"Reboot when the installer asks (it really needs the reboot)",
				"Return to this window and press Re-check",
			},
		}
	}
	return InstallGuide{OS: goos, Title: "Unsupported OS"}
}

// AutoInstall runs the OS-appropriate auto-install command, when one
// exists. Currently only Linux is supported: we shell out to `pactl`
// to load a null sink the application will then bind to.
//
// On other OSes we return an error explaining what the user has to do
// manually; the UI converts that into a prompt + "Open install URL"
// button.
func AutoInstall() error {
	if runtime.GOOS != "linux" {
		return errors.New("vmic: auto-install only supported on Linux (PulseAudio/PipeWire)")
	}
	if _, err := exec.LookPath("pactl"); err != nil {
		return fmt.Errorf("vmic: pactl not found — install pulseaudio-utils or pipewire-pulse")
	}
	// pactl invocation matches guideFor("linux").AutoInstallCmd; we
	// build the argv directly so we don't have to shell-split it at
	// runtime.
	args := []string{
		"load-module", "module-null-sink",
		"sink_name=rstranslator_out",
		`sink_properties=device.description=RST_Translator`,
	}
	out, err := exec.Command("pactl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("vmic: pactl load-module failed: %w (%s)", err, out)
	}
	return nil
}

// Unload removes a previously auto-loaded null sink. We look up the
// module-id by sink name so re-runs don't accumulate modules.
//
// Best-effort: errors are returned but the UI can ignore them — at
// worst the next pactl call complains about a duplicate sink.
func Unload() error {
	if runtime.GOOS != "linux" {
		return nil
	}
	if _, err := exec.LookPath("pactl"); err != nil {
		return nil
	}
	// `pactl list short modules` lines look like:
	//   42  module-null-sink  sink_name=rstranslator_out  ...
	out, err := exec.Command("pactl", "list", "short", "modules").Output()
	if err != nil {
		return err
	}
	const marker = "sink_name=rstranslator_out"
	for _, line := range splitLines(string(out)) {
		if !contains(line, marker) {
			continue
		}
		fields := splitFields(line)
		if len(fields) == 0 {
			continue
		}
		if err := exec.Command("pactl", "unload-module", fields[0]).Run(); err != nil {
			return fmt.Errorf("vmic: unload-module %s: %w", fields[0], err)
		}
	}
	return nil
}

// Small dependency-free string helpers — the install path runs early in
// startup and pulling in strings/bufio elsewhere is fine, but keeping
// this file tight makes it trivial to unit-test on any platform.

func splitLines(s string) []string {
	out := []string{}
	cur := ""
	for _, r := range s {
		if r == '\n' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func splitFields(s string) []string {
	out := []string{}
	cur := ""
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

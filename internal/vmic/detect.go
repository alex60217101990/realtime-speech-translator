package vmic

import (
	"fmt"

	"github.com/gen2brain/malgo"
)

// withTempContext runs fn with a throwaway miniaudio context, suitable
// for one-shot device enumeration outside the Session lifecycle (e.g.
// startup wizard, settings dialog).
func withTempContext(fn func(*malgo.AllocatedContext) error) error {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return fmt.Errorf("vmic: temp context: %w", err)
	}
	defer func() {
		_ = ctx.Uninit()
		ctx.Free()
	}()
	return fn(ctx)
}

// DetectQuick is a Detect call that owns its own miniaudio context.
// Convenient for the startup wizard.
func DetectQuick() ([]PlaybackDevice, error) {
	var out []PlaybackDevice
	err := withTempContext(func(c *malgo.AllocatedContext) error {
		d, err := Detect(c)
		out = d
		return err
	})
	return out, err
}

// AllQuick is the All counterpart of DetectQuick.
func AllQuick() ([]PlaybackDevice, error) {
	var out []PlaybackDevice
	err := withTempContext(func(c *malgo.AllocatedContext) error {
		d, err := All(c)
		out = d
		return err
	})
	return out, err
}

// PlaybackDevice pairs a malgo DeviceID with the detection metadata we
// surface to the UI. The DeviceID is what `playback.Config.DeviceID`
// needs to open the device.
type PlaybackDevice struct {
	ID   malgo.DeviceID
	Name string
	Kind Kind
}

// Detect enumerates playback devices and returns those whose name
// matches a known virtual-audio pattern for the current OS. The slice
// is empty (not nil) if no virtual device is present; callers should
// trigger the install wizard in that case.
//
// The miniaudio context must be initialised by the caller — Session
// already does this for capture/playback, so we accept the same handle.
func Detect(ctx *malgo.AllocatedContext) ([]PlaybackDevice, error) {
	if ctx == nil {
		return nil, fmt.Errorf("vmic: nil miniaudio context")
	}
	infos, err := ctx.Context.Devices(malgo.Playback)
	if err != nil {
		return nil, fmt.Errorf("vmic: enumerate playback devices: %w", err)
	}
	out := make([]PlaybackDevice, 0, 1)
	for i := range infos {
		name := infos[i].Name()
		if k := classify(name); k != KindUnknown {
			out = append(out, PlaybackDevice{
				ID:   infos[i].ID,
				Name: name,
				Kind: k,
			})
		}
	}
	return out, nil
}

// All returns every playback device the OS reports, classified. Used by
// the UI device picker so the user can manually select a non-detected
// virtual mic (e.g. a custom alsa loopback they wired up).
func All(ctx *malgo.AllocatedContext) ([]PlaybackDevice, error) {
	if ctx == nil {
		return nil, fmt.Errorf("vmic: nil miniaudio context")
	}
	infos, err := ctx.Context.Devices(malgo.Playback)
	if err != nil {
		return nil, fmt.Errorf("vmic: enumerate playback devices: %w", err)
	}
	out := make([]PlaybackDevice, len(infos))
	for i := range infos {
		name := infos[i].Name()
		out[i] = PlaybackDevice{
			ID:   infos[i].ID,
			Name: name,
			Kind: classify(name),
		}
	}
	return out, nil
}

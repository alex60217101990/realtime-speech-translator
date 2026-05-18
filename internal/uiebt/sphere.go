package uiebt

import (
	_ "embed"
	"image"
	"log/slog"
	"sync"

	"github.com/hajimehoshi/ebiten/v2"
)

//go:embed sphere.kage
var sphereShaderSrc []byte

// SphereAudio is the per-frame audio snapshot the sphere shader
// consumes. All values are normalised to [0, 1]; the cmd layer
// fills them from uihost.AudioBucket (Bass / Mid / Treble / Rms),
// or leaves them at zero when no Session is bound — the shader
// then renders a calm idle sphere.
type SphereAudio struct {
	Bass   float32
	Mid    float32
	Treble float32
	Rms    float32
}

// Sphere is the audio-reactive UI element drawn into the centre of
// the Main tab. Backed by a Kage fragment shader compiled once at
// boot.
type Sphere struct {
	shader *ebiten.Shader

	mu             sync.RWMutex
	a              SphereAudio
	time           float64
	externalDriven bool // flipped true by the first SetAudio call
}

// NewSphere compiles the Kage shader. Panics on compile failure —
// the shader is a static asset and a parse error means the binary
// is broken.
func NewSphere() *Sphere {
	sh, err := ebiten.NewShader(sphereShaderSrc)
	if err != nil {
		slog.Error("uiebt: failed to compile sphere shader", "err", err)
		panic(err)
	}
	return &Sphere{shader: sh}
}

// Tick advances the shader's Time uniform by dt seconds. Called
// from App.Update once per frame.
func (s *Sphere) Tick(dt float64) {
	s.mu.Lock()
	s.time += dt
	s.mu.Unlock()
}

// SetAudio updates the bass / mid / treble / rms uniforms. Safe
// for concurrent calls from the audio-bucket goroutine. The first
// non-idle call flips externalDriven; from then on the App's idle
// wave stops overwriting these values.
func (s *Sphere) SetAudio(a SphereAudio) {
	s.mu.Lock()
	s.a = a
	if a.Bass != 0 || a.Mid != 0 || a.Treble != 0 || a.Rms != 0 {
		s.externalDriven = true
	}
	s.mu.Unlock()
}

// setAudioInternal is used by the App's idle wave — it does not
// flip externalDriven, so a real SetAudio later still takes over.
func (s *Sphere) setAudioInternal(a SphereAudio) {
	s.mu.Lock()
	s.a = a
	s.mu.Unlock()
}

// Draw paints the sphere into rect r on dst. The shader resolves
// the sphere geometry per-pixel; we provide the centre + base
// radius in pixel space + current audio uniforms.
func (s *Sphere) Draw(dst *ebiten.Image, r image.Rectangle) {
	s.mu.RLock()
	a := s.a
	t := s.time
	s.mu.RUnlock()

	cx := float32(r.Min.X + r.Dx()/2)
	cy := float32(r.Min.Y + r.Dy()/2)
	radius := float32(r.Dy()) / 2.5
	if w := float32(r.Dx()) / 2.5; w < radius {
		radius = w
	}

	// Draw inside a square envelope around the sphere so the
	// shader gets enough pixels for the halo without paying for
	// the empty corners of the centre pane.
	envelope := int(radius * 3.2)
	env := image.Rect(
		int(cx)-envelope/2, int(cy)-envelope/2,
		int(cx)+envelope/2, int(cy)+envelope/2,
	)
	op := &ebiten.DrawRectShaderOptions{}
	op.Uniforms = map[string]any{
		"Time":   float32(t),
		"Center": []float32{cx, cy},
		"Radius": radius,
		"Bass":   a.Bass,
		"Mid":    a.Mid,
		"Treble": a.Treble,
		"Rms":    a.Rms,
	}
	op.GeoM.Translate(float64(env.Min.X), float64(env.Min.Y))
	dst.DrawRectShader(env.Dx(), env.Dy(), s.shader, op)
}

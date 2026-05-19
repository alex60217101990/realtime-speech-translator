package uiebt

import (
	"image"
	"image/color"
	"math"
	"math/rand"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// Particle overlay rendered on TOP of the Kage shader sphere. The
// shader still owns the body, halo, waveform, accent stars; the
// overlay adds curl-noise streams inside the sphere that fade
// over a handful of frames, giving the "magnetic field" feel of
// the ImagineProgramming codepen the user referenced.
//
// Design choices:
//   - Particles + accumulator live on Sphere; populated lazily on
//     first Draw so an idle Sphere costs nothing extra.
//   - Per particle: hash-derived spawn position, random angular
//     jitter every frame (different per particle, different per
//     tick) so vortex trajectories never repeat.
//   - Trails use ebiten.BlendDestinationOut: drawing a translucent
//     white quad over the accum reduces its alpha uniformly, so
//     old strokes fade without painting black over the shader.
//   - Audio scales BOTH the noise amplitude and the per-frame
//     angular jitter, plus the stroke colour saturation, so the
//     louder the voice the more aggressive the swarm.

const (
	overlayParticleCount = 700
	overlayDamping       = 0.94
	overlayVMax          = 5.5
	overlayNoiseScale    = 180.0
	overlayZSpeed        = 1.0 / 380.0
	overlayFadeAlpha     = 28 // 0..255 — bigger = shorter trails
)

type overlayParticle struct {
	x, y         float64
	prevX, prevY float64
	vx, vy       float64
	age          int
	lifespan     int
}

// ensureOverlay sets up the per-Sphere overlay state on first
// Draw and on every accumulator resize. It is cheap to call
// repeatedly — the noise instance + particle slice are reused
// across frames.
func (s *Sphere) ensureOverlay(w, h int, cx, cy, radius float64) {
	if s.overlayNoise == nil {
		s.overlayRNG = rand.New(rand.NewSource(0xC0FFEE))
		s.overlayNoise = newSimplex(func() int { return s.overlayRNG.Intn(256) })
		s.overlayParticles = make([]overlayParticle, overlayParticleCount)
		s.overlayPath = &vector.Path{}
		s.overlayFader = ebiten.NewImage(1, 1)
		s.overlayFader.Fill(color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: overlayFadeAlpha})
	}
	if s.overlayAccum == nil || s.overlayW != w || s.overlayH != h {
		s.overlayAccum = ebiten.NewImage(w, h)
		s.overlayW = w
		s.overlayH = h
		s.overlayCX = cx
		s.overlayCY = cy
		s.overlayR = radius
		for i := range s.overlayParticles {
			s.spawnOverlayParticle(&s.overlayParticles[i])
		}
		return
	}
	s.overlayCX = cx
	s.overlayCY = cy
	s.overlayR = radius
}

// spawnOverlayParticle places one particle uniformly inside the
// circular envelope and resets its kinematics.
func (s *Sphere) spawnOverlayParticle(p *overlayParticle) {
	r := s.overlayR * math.Sqrt(s.overlayRNG.Float64())
	theta := s.overlayRNG.Float64() * 2 * math.Pi
	p.x = s.overlayCX + r*math.Cos(theta)
	p.y = s.overlayCY + r*math.Sin(theta)
	p.prevX = p.x
	p.prevY = p.y
	p.vx = 0
	p.vy = 0
	p.age = 0
	p.lifespan = 600 + s.overlayRNG.Intn(2400)
}

// drawOverlay advances every particle one frame, accumulates its
// trail into the persistent overlay image, then blits the overlay
// onto dst (over the already-drawn shader sphere).
func (s *Sphere) drawOverlay(dst *ebiten.Image, env image.Rectangle, audio SphereAudio, t float64) {
	cx := float64(env.Dx()) / 2
	cy := float64(env.Dy()) / 2
	radius := math.Min(float64(env.Dx()), float64(env.Dy())) / 2.6

	s.ensureOverlay(env.Dx(), env.Dy(), cx, cy, radius)

	// Trail fade: draw the cached 1×1 white quad scaled to the
	// accum size with destination-out blend → reduces alpha of
	// every accumulated pixel by overlayFadeAlpha/255.
	fadeOp := &ebiten.DrawImageOptions{}
	fadeOp.GeoM.Scale(float64(s.overlayW), float64(s.overlayH))
	fadeOp.Blend = ebiten.BlendDestinationOut
	s.overlayAccum.DrawImage(s.overlayFader, fadeOp)

	// Step + build the stroke path.
	s.overlayPath.Reset()
	s.stepOverlay(t, audio)

	col := s.overlayStrokeColor(audio)
	var cs ebiten.ColorScale
	cs.ScaleWithColor(col)
	vector.StrokePath(s.overlayAccum, s.overlayPath,
		&vector.StrokeOptions{Width: 1.2, LineCap: vector.LineCapRound},
		&vector.DrawPathOptions{AntiAlias: true, ColorScale: cs},
	)

	// Blit accum on top of the shader sphere with additive blend
	// so the streams brighten the body instead of dimming it.
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Translate(float64(env.Min.X), float64(env.Min.Y))
	op.Blend = ebiten.BlendLighter
	dst.DrawImage(s.overlayAccum, op)
}

func (s *Sphere) stepOverlay(t float64, audio SphereAudio) {
	rms := float64(audio.Rms)
	bass := float64(audio.Bass)
	noiseAmp := 1.0 + 4.0*rms
	jitterAmp := 0.30 + 2.2*bass
	// Radial blast amplitude: loud frames push every particle
	// outward from the sphere centre so a yell visibly breaks the
	// swarm apart. Pure noise jitter alone reads as "more
	// shimmery" not "shattered" — the radial term gives the
	// directional shock the user asked for.
	blast := rms*4.5 + bass*2.5
	z := t * overlayZSpeed * (1.0 + 0.6*rms)
	r2max := s.overlayR * s.overlayR

	for i := range s.overlayParticles {
		p := &s.overlayParticles[i]
		p.age++
		if p.age > p.lifespan {
			s.spawnOverlayParticle(p)
		}

		a := s.overlayRNG.Float64() * 2 * math.Pi
		j := (s.overlayRNG.Float64() - 0.5) * jitterAmp

		nx := p.x / overlayNoiseScale
		ny := p.y / overlayNoiseScale
		dvx := j*math.Sin(a) + noiseAmp*s.overlayNoise.Sample(nx, ny, -z)
		dvy := j*math.Cos(a) + noiseAmp*s.overlayNoise.Sample(nx, ny, z)

		// Radial outward kick scaled by audio. Negligible at idle
		// because blast ≈ 0; explosive at full speech volume.
		if blast > 0.05 {
			dx := p.x - s.overlayCX
			dy := p.y - s.overlayCY
			dist := math.Sqrt(dx*dx + dy*dy)
			if dist > 1 {
				dvx += (dx / dist) * blast
				dvy += (dy / dist) * blast
			}
		}

		p.vx += dvx
		p.vy += dvy

		sp := math.Sqrt(p.vx*p.vx + p.vy*p.vy)
		if sp > overlayVMax {
			p.vx *= overlayVMax / sp
			p.vy *= overlayVMax / sp
		}

		p.prevX = p.x
		p.prevY = p.y
		p.x += p.vx * overlayDamping
		p.y += p.vy * overlayDamping
		p.vx *= overlayDamping
		p.vy *= overlayDamping

		dx := p.x - s.overlayCX
		dy := p.y - s.overlayCY
		if dx*dx+dy*dy > r2max {
			s.spawnOverlayParticle(p)
			continue
		}

		s.overlayPath.MoveTo(float32(p.prevX), float32(p.prevY))
		s.overlayPath.LineTo(float32(p.x), float32(p.y))
	}
}

// overlayStrokeColor brightens + shifts toward magenta as the
// audio energy rises. Idle stays faint cyan so the shader's own
// star field reads clearly through the overlay.
func (s *Sphere) overlayStrokeColor(a SphereAudio) color.NRGBA {
	rms := float32(a.Rms)
	r := uint8(0x5F + uint8(float32(0x80)*rms))
	g := uint8(float32(0xC8) * (1 - 0.3*rms))
	b := uint8(0xFF)
	alpha := uint8(80 + 120*rms)
	if alpha > 220 {
		alpha = 220
	}
	return color.NRGBA{R: r, G: g, B: b, A: alpha}
}

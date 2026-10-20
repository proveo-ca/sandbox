// SPEC: _spec/internal/choiceui/flock.puml
package choiceui

import (
	"math"
	"strings"

	"github.com/gdamore/tcell/v2"
)

const (
	flockRows = 4
	flockCost = flockRows + 2
)

const (
	flockSeed       = 0xcb17d5
	flockPerception = 16.0
	flockSeparation = 5.0
	flockMargin     = 8.0
	flockMinSpeed   = 0.55
	flockMaxSpeed   = 1.05
	flockMaxAccel   = 0.06
	flockMaxDelta   = int(5 * animFrame / flockFrame)
)

type bird struct{ x, y, vx, vy float64 }

type flockSim struct {
	birds    []bird
	rng      uint64
	lastTick int
	dotsW    int
	dotsH    int
}

// newFlockSim seeds a flock for a cols-by-flockRows cell region.
func newFlockSim(cols int) *flockSim {
	s := &flockSim{rng: flockSeed}
	s.resize(cols)
	return s
}

func (s *flockSim) next() float64 {
	s.rng += 0x9e3779b97f4a7c15
	x := s.rng
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return float64(x>>11) / (1 << 53)
}

func (s *flockSim) resize(cols int) {
	w, h := cols*2, flockRows*4
	if s.birds != nil && s.dotsW == w && s.dotsH == h {
		return
	}
	if s.birds != nil && s.dotsW > 0 && s.dotsH > 0 {
		sx, sy := float64(w)/float64(s.dotsW), float64(h)/float64(s.dotsH)
		for i := range s.birds {
			s.birds[i].x *= sx
			s.birds[i].y *= sy
		}
		s.dotsW, s.dotsH = w, h
		return
	}
	s.dotsW, s.dotsH = w, h
	n := clampInt(cols/2, 20, 56)
	s.birds = make([]bird, n)
	for i := range s.birds {
		a := s.next() * 2 * math.Pi
		s.birds[i] = bird{
			x:  s.next() * float64(w),
			y:  s.next() * float64(h),
			vx: math.Cos(a) * flockMinSpeed,
			vy: math.Sin(a) * flockMinSpeed,
		}
	}
}

// advance moves the flock to tick, capped per wake.
func (s *flockSim) advance(tick int) {
	if tick <= s.lastTick {
		return
	}
	steps := tick - s.lastTick
	if steps > flockMaxDelta {
		steps = flockMaxDelta
	}
	for ; steps > 0; steps-- {
		s.step()
	}
	s.lastTick = tick
}

func (s *flockSim) step() {
	prev := append([]bird(nil), s.birds...)
	for i := range s.birds {
		b := &s.birds[i]
		var n int
		var avx, avy, cx, cy, sx, sy float64
		for j := range prev {
			if j == i {
				continue
			}
			dx, dy := prev[j].x-b.x, prev[j].y-b.y
			d2 := dx*dx + dy*dy
			if d2 > flockPerception*flockPerception {
				continue
			}
			n++
			avx, avy = avx+prev[j].vx, avy+prev[j].vy
			cx, cy = cx+prev[j].x, cy+prev[j].y
			if d2 < flockSeparation*flockSeparation && d2 > 0.001 {
				sx, sy = sx-dx/d2, sy-dy/d2
			}
		}
		ax, ay := sx*0.08, sy*0.08
		if n > 0 {
			fn := float64(n)
			ax += (avx/fn - b.vx) * 0.04
			ay += (avy/fn - b.vy) * 0.04
			ax += (cx/fn - b.x) * 0.005
			ay += (cy/fn - b.y) * 0.005
		}
		ax += flockBoundary(b.x, float64(s.dotsW))
		ay += flockBoundary(b.y, float64(s.dotsH))
		if m := math.Hypot(ax, ay); m > flockMaxAccel {
			ax, ay = ax/m*flockMaxAccel, ay/m*flockMaxAccel
		}
		b.vx, b.vy = flockSpeed(b.vx+ax*flockStep, b.vy+ay*flockStep)
		b.x, b.y = b.x+b.vx*flockStep, b.y+b.vy*flockStep
		if b.x < 0 {
			b.x, b.vx = 0, math.Abs(b.vx)
		}
		if b.x > float64(s.dotsW-1) {
			b.x, b.vx = float64(s.dotsW-1), -math.Abs(b.vx)
		}
		if b.y < 0 {
			b.y, b.vy = 0, math.Abs(b.vy)
		}
		if b.y > float64(s.dotsH-1) {
			b.y, b.vy = float64(s.dotsH-1), -math.Abs(b.vy)
		}
	}
}

func flockBoundary(pos, size float64) float64 {
	switch {
	case pos < flockMargin:
		return (flockMargin - pos) * 0.02
	case pos > size-flockMargin:
		return (size - flockMargin - pos) * 0.02
	}
	return 0
}

func flockSpeed(vx, vy float64) (float64, float64) {
	m := math.Hypot(vx, vy)
	switch {
	case m > flockMaxSpeed:
		return vx / m * flockMaxSpeed, vy / m * flockMaxSpeed
	case m < flockMinSpeed && m > 0:
		return vx / m * flockMinSpeed, vy / m * flockMinSpeed
	case m == 0:
		return flockMinSpeed, 0
	}
	return vx, vy
}

var flockBrailleBits = [2][4]uint8{{1, 2, 4, 64}, {8, 16, 32, 128}}

// frame packs the current dot grid into one string per row.
func (s *flockSim) frame(tier GlyphTier) []string {
	if tier == GlyphsOff || s.dotsW < 2 {
		return nil
	}
	cols := s.dotsW / 2
	masks := make([]uint8, cols*flockRows)
	counts := make([]uint8, cols*flockRows)
	for _, b := range s.birds {
		dx, dy := int(b.x), int(b.y)
		if dx < 0 || dy < 0 || dx >= s.dotsW || dy >= s.dotsH {
			continue
		}
		i := (dy/4)*cols + dx/2
		masks[i] |= flockBrailleBits[dx%2][dy%4]
		counts[i]++
	}
	out := make([]string, flockRows)
	for row := 0; row < flockRows; row++ {
		var sb strings.Builder
		for col := 0; col < cols; col++ {
			i := row*cols + col
			switch {
			case masks[i] == 0:
				sb.WriteByte(' ')
			case tier == GlyphsASCII && counts[i] <= 2:
				sb.WriteByte('.')
			case tier == GlyphsASCII:
				sb.WriteByte('*')
			default:
				sb.WriteRune(rune(0x2800 + int(masks[i])))
			}
		}
		out[row] = strings.TrimRight(sb.String(), " ")
	}
	return out
}

// drawFlock paints the flock at y0 in a cols-wide region starting at x=2.
func drawFlock(s tcell.Screen, y0, cols, tick int, tier GlyphTier, style tcell.Style, sim *flockSim) {
	if tier == GlyphsOff || cols < 20 || sim == nil {
		return
	}
	sim.resize(cols)
	sim.advance(tick)
	for i, line := range sim.frame(tier) {
		newPen(s, 2, y0+i).write(style, line)
	}
}

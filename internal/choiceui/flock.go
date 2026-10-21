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
	flockMargin     = 3.0
	flockMinSpeed   = 0.55
	flockMaxSpeed   = 1.05
	flockTurn       = 0.4 // radians per strip frame, scaled by flockStep
	flockMaxDelta   = 1
	flockFacings    = 8
	flockCols       = 80
	flockDotRadius  = 0.55
	flockFlee       = 14.0
	flockHawkWeight = 6.0
	flockHawkScale  = 2.0
)

type bird struct{ x, y, heading, speed float64 }

type flockSim struct {
	birds    []bird
	hawk     bird
	rng      uint64
	lastTick int
	dotsW    int
	dotsH    int
}

func flockSpan(cols int) int {
	if cols > flockCols {
		return flockCols
	}
	return cols
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
		s.hawk.x *= sx
		s.hawk.y *= sy
		s.dotsW, s.dotsH = w, h
		return
	}
	s.dotsW, s.dotsH = w, h
	n := clampInt(cols/2, 20, 56)
	s.birds = make([]bird, n)
	for i := range s.birds {
		a := s.next() * 2 * math.Pi
		s.birds[i] = bird{
			x:       s.next() * float64(w),
			y:       s.next() * float64(h),
			heading: a,
			speed:   flockMinSpeed,
		}
	}
	s.hawk = bird{x: float64(w) / 2, y: float64(h) / 2, heading: 0, speed: flockMaxSpeed}
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
	hx, hy := s.hawk.x, s.hawk.y
	s.hawk = s.steerHawk(prev)
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
			avx += math.Cos(prev[j].heading)
			avy += math.Sin(prev[j].heading)
			cx, cy = cx+prev[j].x, cy+prev[j].y
			if d2 < flockSeparation*flockSeparation && d2 > 0.001 {
				sx, sy = sx-dx/d2, sy-dy/d2
			}
		}
		ax, ay := sx*0.08, sy*0.08
		if n > 0 {
			fn := float64(n)
			ax += (avx/fn - math.Cos(b.heading)) * 0.04
			ay += (avy/fn - math.Sin(b.heading)) * 0.04
			ax += (cx/fn - b.x) * 0.005
			ay += (cy/fn - b.y) * 0.005
		}
		ax += flockBoundary(b.x, float64(s.dotsW))
		ay += flockBoundary(b.y, float64(s.dotsH))
		if dx, dy := b.x-hx, b.y-hy; dx*dx+dy*dy < flockFlee*flockFlee && dx*dx+dy*dy > 0.001 {
			ax += dx / (dx*dx + dy*dy) * flockHawkWeight
			ay += dy / (dx*dx + dy*dy) * flockHawkWeight
		}
		desired := b.heading
		if math.Hypot(ax, ay) > 1e-9 {
			desired = math.Atan2(ay, ax)
		}
		b.heading = flockTurnTowards(b.heading, desired, flockTurn*flockStep)
		b.x += math.Cos(b.heading) * b.speed * flockStep
		b.y += math.Sin(b.heading) * b.speed * flockStep
		b.x = flockClamp(b.x, float64(s.dotsW))
		b.y = flockClamp(b.y, float64(s.dotsH))
	}
}

func (s *flockSim) steerHawk(prev []bird) bird {
	h := s.hawk
	var nearest float64 = -1
	var tx, ty float64
	for _, b := range prev {
		dx, dy := b.x-h.x, b.y-h.y
		d2 := dx*dx + dy*dy
		if nearest < 0 || d2 < nearest {
			nearest, tx, ty = d2, b.x, b.y
		}
	}
	desired := h.heading
	if nearest > 0.001 {
		desired = math.Atan2(ty-h.y, tx-h.x)
	}
	h.heading = flockTurnTowards(h.heading, desired, flockTurn*flockStep)
	h.x += math.Cos(h.heading) * h.speed * flockStep
	h.y += math.Sin(h.heading) * h.speed * flockStep
	h.x, h.y = flockClamp(h.x, float64(s.dotsW)), flockClamp(h.y, float64(s.dotsH))
	return h
}

func flockClamp(pos, size float64) float64 {
	if pos < 0 {
		return 0
	}
	if pos > size-1 {
		return size - 1
	}
	return pos
}

func flockTurnTowards(from, to, limit float64) float64 {
	d := math.Mod(to-from, 2*math.Pi)
	if d > math.Pi {
		d -= 2 * math.Pi
	} else if d < -math.Pi {
		d += 2 * math.Pi
	}
	if d > limit {
		d = limit
	} else if d < -limit {
		d = -limit
	}
	return from + d
}

func flockBoundary(pos, size float64) float64 {
	switch {
	case pos < flockMargin:
		return (flockMargin - pos) / flockMargin
	case pos > size-flockMargin:
		return (size - flockMargin - pos) / flockMargin
	}
	return 0
}

// flockBody is the east-facing chevron rotated through eight headings.
// Offsets are nose, two wings, body, tail.
var flockBody = [flockFacings][5][2]int{
	{{2, 0}, {1, 1}, {1, -1}, {0, 0}, {-1, 0}},
	{{1, 1}, {0, 1}, {1, 0}, {0, 0}, {-1, -1}},
	{{0, 2}, {-1, 1}, {1, 1}, {0, 0}, {0, -1}},
	{{-1, 1}, {-1, 0}, {0, 1}, {0, 0}, {1, -1}},
	{{-2, 0}, {-1, -1}, {-1, 1}, {0, 0}, {1, 0}},
	{{-1, -1}, {0, -1}, {-1, 0}, {0, 0}, {1, 1}},
	{{0, -2}, {1, -1}, {-1, -1}, {0, 0}, {0, 1}},
	{{1, -1}, {1, 0}, {0, -1}, {0, 0}, {-1, 1}},
}

func flockBin(heading float64) int {
	a := math.Mod(heading, 2*math.Pi)
	if a < 0 {
		a += 2 * math.Pi
	}
	return int(math.Floor((a+math.Pi/8)/(math.Pi/4))) % flockFacings
}

var flockBrailleBits = [2][4]uint8{{1, 2, 4, 64}, {8, 16, 32, 128}}

func (s *flockSim) stamp(masks, counts, hawk []uint8, cols int, b bird, scale float64, isHawk bool) {
	fx, fy := b.x-math.Floor(b.x), b.y-math.Floor(b.y)
	ax, ay := int(math.Floor(b.x)), int(math.Floor(b.y))
	r2 := flockDotRadius * flockDotRadius
	for _, off := range flockBody[flockBin(b.heading)] {
		px := float64(ax+int(math.Round(float64(off[0])*scale))) + fx
		py := float64(ay+int(math.Round(float64(off[1])*scale))) + fy
		x0, y0 := int(math.Floor(px-flockDotRadius)), int(math.Floor(py-flockDotRadius))
		x1, y1 := int(math.Ceil(px+flockDotRadius)), int(math.Ceil(py+flockDotRadius))
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				if x < 0 || y < 0 || x >= s.dotsW || y >= s.dotsH {
					continue
				}
				dx, dy := float64(x)-px, float64(y)-py
				if dx*dx+dy*dy > r2 {
					continue
				}
				i := (y/4)*cols + x/2
				masks[i] |= flockBrailleBits[x%2][y%4]
				counts[i]++
				if isHawk {
					hawk[i] = 1
				}
			}
		}
	}
}

// frame packs the current dot grid into one string per row.
func (s *flockSim) frame(tier GlyphTier) []string {
	lines, _, _ := s.raster(tier)
	return lines
}

func (s *flockSim) raster(tier GlyphTier) (lines []string, hawk [][]bool, cols int) {
	if tier == GlyphsOff || s.dotsW < 2 {
		return nil, nil, 0
	}
	cols = s.dotsW / 2
	masks := make([]uint8, cols*flockRows)
	counts := make([]uint8, cols*flockRows)
	who := make([]uint8, cols*flockRows)
	for _, b := range s.birds {
		s.stamp(masks, counts, who, cols, b, 1, false)
	}
	s.stamp(masks, counts, who, cols, s.hawk, flockHawkScale, true)
	lines = make([]string, flockRows)
	hawk = make([][]bool, flockRows)
	for row := 0; row < flockRows; row++ {
		var sb strings.Builder
		flags := make([]bool, cols)
		for col := 0; col < cols; col++ {
			i := row*cols + col
			flags[col] = who[i] != 0
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
		lines[row] = strings.TrimRight(sb.String(), " ")
		if n := len([]rune(lines[row])); n < len(flags) {
			flags = flags[:n]
		}
		hawk[row] = flags
	}
	return lines, hawk, cols
}

// drawFlock paints the flock at y0 in a cols-wide region starting at x=2.
func drawFlock(s tcell.Screen, y0, cols, tick int, tier GlyphTier, birdStyle, hawkStyle tcell.Style, sim *flockSim) {
	if tier == GlyphsOff || cols < 20 || sim == nil {
		return
	}
	sim.resize(cols)
	sim.advance(tick)
	lines, who, _ := sim.raster(tier)
	for i, line := range lines {
		pen := newPen(s, 2, y0+i)
		for col, r := range line {
			style := birdStyle
			if col < len(who[i]) && who[i][col] {
				style = hawkStyle
			}
			pen.write(style, string(r))
		}
	}
}

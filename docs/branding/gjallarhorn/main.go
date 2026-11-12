// gjallarhorn: procedurally renders Heimdall's horn for gjallarOS as a faceted,
// lit SVG in the jods jade icon style — a great carved aurochs horn held in a
// fist like an emblem horn: mouthpiece left, bell right and turned to the viewer.
//
// Model space: X = right, Y = up, Z = towards the viewer.
//
//	go run . [-variant gold|ember|patina] [out.svg]
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
)

type V3 struct{ X, Y, Z float64 }

func (a V3) Add(b V3) V3      { return V3{a.X + b.X, a.Y + b.Y, a.Z + b.Z} }
func (a V3) Sub(b V3) V3      { return V3{a.X - b.X, a.Y - b.Y, a.Z - b.Z} }
func (a V3) Mul(k float64) V3 { return V3{a.X * k, a.Y * k, a.Z * k} }
func (a V3) Dot(b V3) float64 { return a.X*b.X + a.Y*b.Y + a.Z*b.Z }
func (a V3) Cross(b V3) V3    { return V3{a.Y*b.Z - a.Z*b.Y, a.Z*b.X - a.X*b.Z, a.X*b.Y - a.Y*b.X} }
func (a V3) Len() float64     { return math.Sqrt(a.Dot(a)) }
func (a V3) Norm() V3 {
	l := a.Len()
	if l == 0 {
		return a
	}
	return a.Mul(1 / l)
}

// ---- scene ------------------------------------------------------------------

const cx, cy = 63.0, 58.0

var (
	camDir = V3{0, 0.26, 1}.Norm() // towards the viewer, a little from above
	up     = V3{0, 1, 0}.Sub(camDir.Mul(camDir.Y)).Norm()
	right  = up.Cross(camDir).Norm()
	light  = right.Mul(-0.55).Add(up.Mul(0.6)).Add(camDir.Mul(0.7)).Norm() // upper left, like the jade
	halfV  = light.Add(camDir).Norm()
)

func project(p V3) (float64, float64) { return cx + p.Dot(right), cy - p.Dot(up) }
func depth(p V3) float64              { return p.Dot(camDir) }

// ---- colour -----------------------------------------------------------------

type stop struct{ t, r, g, b float64 }

func hex(t float64, s string) stop {
	var r, g, b int
	fmt.Sscanf(s, "#%02x%02x%02x", &r, &g, &b)
	return stop{t, float64(r), float64(g), float64(b)}
}

// Gjallarhorn: ivory bone, a metal band, dark leather, warm skin.
var leatherRamp = []stop{hex(0, "#0E0703"), hex(0.35, "#33190C"), hex(0.65, "#633A20"), hex(0.88, "#9C6B45"), hex(1, "#CDA27C")}
var skinRamp = []stop{hex(0, "#1F0E07"), hex(0.3, "#5C301B"), hex(0.55, "#A06844"), hex(0.75, "#D2A07A"), hex(0.9, "#F0CDB0"), hex(1, "#FFF0E3")}
var throatRamp = []stop{hex(0, "#0A0603"), hex(0.5, "#24170A"), hex(1, "#4E3819")}

// Horn versions for the built-in themes (THEMEBRAIN section 0, Product
// marks). A version changes only the metal, the bone and the outline;
// leather, skin and throat are the same in all of them. gold is the
// default and the one in the README.
type variant struct {
	gold, ivory []stop
	ink         string
}

var ivoryBone = []stop{hex(0, "#1E1309"), hex(0.28, "#5A3E1E"), hex(0.5, "#A2804A"), hex(0.7, "#DCC593"), hex(0.88, "#F8EDD2"), hex(1, "#FFFFF6")}

var variants = map[string]variant{
	"gold": {
		gold:  []stop{hex(0, "#251500"), hex(0.28, "#6D4602"), hex(0.5, "#B07C32"), hex(0.7, "#E3B464"), hex(0.88, "#FFCB8C"), hex(1, "#FFF5EC")},
		ivory: ivoryBone,
		ink:   "#1C211B",
	},
	"ember": {
		gold:  []stop{hex(0, "#280105"), hex(0.28, "#571D1D"), hex(0.5, "#96452D"), hex(0.7, "#ED935C"), hex(0.88, "#FDD3B3"), hex(1, "#FFF5EC")},
		ivory: ivoryBone,
		ink:   "#1C211B",
	},
	"patina": {
		gold:  []stop{hex(0, "#001A0F"), hex(0.28, "#1D5539"), hex(0.5, "#B07C32"), hex(0.7, "#E3B464"), hex(0.88, "#FFCB8C"), hex(1, "#FFF5EC")},
		ivory: []stop{hex(0, "#001A0F"), hex(0.28, "#454B42"), hex(0.5, "#909689"), hex(0.7, "#C4CABD"), hex(0.88, "#EBEEE6"), hex(1, "#F6F7F2")},
		ink:   "#1C211B",
	},
}

// The chosen version, set in main before anything is drawn.
var (
	goldRamp, ivoryRamp []stop
	ink                 string
)

func ramp(rs []stop, t float64) string {
	t = math.Max(0, math.Min(1, t))
	for i := 1; i < len(rs); i++ {
		if t <= rs[i].t {
			a, b := rs[i-1], rs[i]
			k := (t - a.t) / (b.t - a.t)
			return fmt.Sprintf("#%02X%02X%02X", int(a.r+(b.r-a.r)*k+0.5), int(a.g+(b.g-a.g)*k+0.5), int(a.b+(b.b-a.b)*k+0.5))
		}
	}
	l := rs[len(rs)-1]
	return fmt.Sprintf("#%02X%02X%02X", int(l.r), int(l.g), int(l.b))
}

func lightness(n V3, amb, diff, spec, shin float64) float64 {
	return amb + diff*math.Max(0, n.Dot(light)) + spec*math.Pow(math.Max(0, n.Dot(halfV)), shin)
}

// ---- drawing ----------------------------------------------------------------

type item struct {
	depth float64
	svg   string
	lead  bool
}

func pts2s(pts []V3) string {
	var b strings.Builder
	for i, q := range pts {
		x, y := project(q)
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%.2f,%.2f", x, y)
	}
	return b.String()
}

func polygon(pts []V3, fill, stroke string, sw float64) string {
	s := ""
	if stroke != "" {
		s = fmt.Sprintf(` stroke="%s" stroke-width="%.2f"`, stroke, sw)
	}
	return fmt.Sprintf(`<polygon points="%s" fill="%s"%s/>`, pts2s(pts), fill, s)
}

func sortGroups(ps []item) []item {
	type grp struct {
		d  float64
		ps []item
	}
	var gs []grp
	for i := 0; i < len(ps); {
		j := i + 1
		for j < len(ps) && !ps[j].lead {
			j++
		}
		gs = append(gs, grp{ps[i].depth, ps[i:j]})
		i = j
	}
	sort.SliceStable(gs, func(a, b int) bool { return gs[a].d < gs[b].d })
	var out []item
	for _, g := range gs {
		out = append(out, g.ps...)
	}
	return out
}

func newell(pts []V3) V3 {
	var n V3
	for i := range pts {
		a, b := pts[i], pts[(i+1)%len(pts)]
		n.X += (a.Y - b.Y) * (a.Z + b.Z)
		n.Y += (a.Z - b.Z) * (a.X + b.X)
		n.Z += (a.X - b.X) * (a.Y + b.Y)
	}
	return n.Norm()
}

func centroid(pts []V3) V3 {
	var c V3
	for _, p := range pts {
		c = c.Add(p)
	}
	return c.Mul(1 / float64(len(pts)))
}

func catmullChain(cp []V3, per int) []V3 {
	ext := append([]V3{cp[0].Mul(2).Sub(cp[1])}, cp...)
	ext = append(ext, cp[len(cp)-1].Mul(2).Sub(cp[len(cp)-2]))
	var out []V3
	for i := 1; i+2 < len(ext); i++ {
		p0, p1, p2, p3 := ext[i-1], ext[i], ext[i+1], ext[i+2]
		for s := 0; s < per; s++ {
			t := float64(s) / float64(per)
			t2, t3 := t*t, t*t*t
			f := func(a, b, c, d float64) float64 {
				return 0.5 * (2*b + (-a+c)*t + (2*a-5*b+4*c-d)*t2 + (-a+3*b-3*c+d)*t3)
			}
			out = append(out, V3{f(p0.X, p1.X, p2.X, p3.X), f(p0.Y, p1.Y, p2.Y, p3.Y), f(p0.Z, p1.Z, p2.Z, p3.Z)})
		}
	}
	return append(out, cp[len(cp)-1])
}

func smooth(e0, e1, x float64) float64 {
	t := math.Max(0, math.Min(1, (x-e0)/(e1-e0)))
	return t * t * (3 - 2*t)
}

// ---- lit tubes --------------------------------------------------------------

type frame struct {
	P, T, S, U V3
	r, u, s    float64 // radius, 0..1 along, arc length
}

type tube struct {
	path   []V3
	radius func(u float64) float64
	mat    func(u float64) []stop
	// per-facet brightness tweak: u along, s arc length, a = angle across (-1..1)
	bright func(u, s, a float64) float64
	edge   float64
}

func (tb tube) frames() []frame {
	n := len(tb.path)
	cum := make([]float64, n)
	for i := 1; i < n; i++ {
		cum[i] = cum[i-1] + tb.path[i].Sub(tb.path[i-1]).Len()
	}
	out := make([]frame, n)
	for i := 0; i < n; i++ {
		a, b := tb.path[max(i-1, 0)], tb.path[min(i+1, n-1)]
		T := b.Sub(a).Norm()
		S := T.Cross(camDir).Norm()
		if i > 0 && S.Dot(out[i-1].S) < 0 {
			S = S.Mul(-1)
		}
		U := S.Cross(T).Norm()
		if U.Dot(camDir) < 0 {
			U = U.Mul(-1)
		}
		u := cum[i] / cum[n-1]
		out[i] = frame{tb.path[i], T, S, U, tb.radius(u), u, cum[i]}
	}
	return out
}

var strips = []float64{-1, -0.9, -0.74, -0.54, -0.3, -0.06, 0.18, 0.42, 0.64, 0.82, 0.94, 1}

func (tb tube) items() []item {
	fr := tb.frames()
	var out []item
	for i := 0; i+1 < len(fr); i++ {
		a, b := fr[i], fr[i+1]
		um, sm := (a.u+b.u)/2, (a.s+b.s)/2
		d := depth(a.P.Add(b.P).Mul(0.5))
		e := tb.edge
		out = append(out, item{d, polygon([]V3{a.P.Add(a.S.Mul(a.r + e)), b.P.Add(b.S.Mul(b.r + e)), b.P.Sub(b.S.Mul(b.r + e)), a.P.Sub(a.S.Mul(a.r + e))}, ink, "", 0), true})
		mat := tb.mat(um)
		U := a.U.Add(b.U).Norm()
		S := a.S.Add(b.S).Norm()
		for k := 0; k+1 < len(strips); k++ {
			s0, s1 := strips[k], strips[k+1]
			am := (s0 + s1) / 2
			al := math.Asin(am)
			nrm := U.Mul(math.Cos(al)).Add(S.Mul(math.Sin(al))).Norm()
			c := ramp(mat, lightness(nrm, 0.14, 0.64, 0.6, 26)+tb.bright(um, sm, am))
			out = append(out, item{d, polygon([]V3{a.P.Add(a.S.Mul(a.r * s0)), b.P.Add(b.S.Mul(b.r * s0)), b.P.Add(b.S.Mul(b.r * s1)), a.P.Add(a.S.Mul(a.r * s1))}, c, c, 0.28), false})
		}
	}
	return out
}

func ring(f frame, r float64, n int) []V3 {
	N := f.T.Cross(f.S).Norm()
	var pts []V3
	for k := 0; k < n; k++ {
		a := 2 * math.Pi * float64(k) / float64(n)
		pts = append(pts, f.P.Add(f.S.Mul(r*math.Cos(a))).Add(N.Mul(r*math.Sin(a))))
	}
	return pts
}

// ---- the horn ---------------------------------------------------------------

var bands = [][2]float64{{0, 0.085}, {0.27, 0.33}, {0.58, 0.64}, {0.9, 1}}

func inBand(u float64) (bool, float64) {
	for _, b := range bands {
		if u >= b[0] && u <= b[1] {
			return true, (u - b[0]) / (b[1] - b[0])
		}
	}
	return false, 0
}

func hornPath() []V3 {
	// a great aurochs horn bent into a deep crescent: mouthpiece on the left,
	// sweeping down through the fist and rising to a bell turned to the viewer
	return catmullChain([]V3{
		{-47, 9, -5},
		{-42, -7, -3},
		{-30, -20, -1},
		{-11, -27, 1},
		{9, -24, 3},
		{26, -12, 6},
		{37, 4, 10},
		{41, 19, 16},
	}, 28)
}

func hornRadius(u float64) float64 {
	return 2.4 + 12.6*math.Pow(u, 1.6) + 3.6*smooth(0.88, 1, u)
}

func hornBright(u, s, a float64) float64 {
	if ok, f := inBand(u); ok {
		if f < 0.16 || f > 0.84 { // raised lips
			return 0.14
		}
		if f < 0.22 || f > 0.78 {
			return -0.18 // grooves beside the lips
		}
		// twisted rope of gold
		return 0.13 * math.Sin(2*math.Pi*(s/2.4+a*0.9))
	}
	// carved growth ridges: a rounded crest and a sharp dark groove,
	// spaced wider as the horn widens — the horn's answer to Genbu's scute rings
	ph := s / (2.6 + 2.2*u)
	ph -= math.Floor(ph)
	if ph > 0.88 {
		return -0.26
	}
	return 0.07 * math.Cos(2*math.Pi*(ph-0.44))
}

func runes(fr []frame) string {
	// "GJALLAR" in the elder futhark, inlaid in gold on the wide part of the horn
	glyphs := map[rune][][][2]float64{
		'G': {{{-1, -1}, {1, 1}}, {{-1, 1}, {1, -1}}},
		'J': {{{-0.1, 1}, {-0.75, 0.45}, {-0.1, -0.1}}, {{0.1, 0.1}, {0.75, -0.45}, {0.1, -1}}},
		'A': {{{-0.5, 1}, {-0.5, -1}}, {{-0.5, 1}, {0.5, 0.45}}, {{-0.5, 0.35}, {0.5, -0.2}}},
		'L': {{{-0.4, 1}, {-0.4, -1}}, {{-0.4, 1}, {0.55, 0.4}}},
		'R': {{{-0.5, 1}, {-0.5, -1}}, {{-0.5, 1}, {0.45, 0.5}, {-0.5, 0}, {0.5, -1}}},
	}
	word := "GJALLAR"
	var b strings.Builder
	u0, u1 := 0.665, 0.885
	for gi, ch := range word {
		u := u0 + (u1-u0)*(float64(gi)+0.5)/float64(len(word))
		i := 0
		for i+1 < len(fr) && fr[i].u < u {
			i++
		}
		f := fr[i]
		h := f.r * 0.32
		w := h * 0.6
		c := f.P.Add(f.U.Mul(f.r * 0.95)).Add(f.S.Mul(f.r * 0.08))
		for _, pass := range []struct {
			col string
			w   float64
		}{{ink, 1.05}, {ramp(goldRamp, 0.8), 0.5}} {
			for _, stroke := range glyphs[ch] {
				var pts []string
				for _, q := range stroke {
					p := c.Add(f.T.Mul(q[0] * w)).Add(f.S.Mul(q[1] * h))
					x, y := project(p)
					pts = append(pts, fmt.Sprintf("%.2f,%.2f", x, y))
				}
				fmt.Fprintf(&b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="%.2f" stroke-linecap="round" stroke-linejoin="round"/>
`, strings.Join(pts, " "), pass.col, pass.w)
			}
		}
	}
	return b.String()
}

// ---- the fist and forearm ---------------------------------------------------

func handItems(fr []frame) (arm, back, fingers, thumb []item, edge string) {
	at := func(u float64) frame {
		i := 0
		for i+1 < len(fr) && fr[i].u < u {
			i++
		}
		return fr[i]
	}
	g := at(0.45)
	sup := g.S
	if sup.Dot(up) < 0 {
		sup = sup.Mul(-1)
	}
	tAx := g.T
	if tAx.Dot(right) < 0 {
		tAx = tAx.Mul(-1)
	}
	deg := math.Pi / 180
	around := func(t, theta, rr float64) V3 { // theta 0 = facing us, +90 = over the top
		return g.P.Add(tAx.Mul(t)).Add(g.U.Mul(rr * math.Cos(theta*deg))).Add(sup.Mul(rr * math.Sin(theta*deg)))
	}
	skinTube := func(path []V3, r func(u float64) float64) tube {
		return tube{path: path, edge: 0.7, radius: r,
			mat:    func(u float64) []stop { return skinRamp },
			bright: func(u, s, a float64) float64 { return 0 }}
	}
	rg := g.r
	const rf = 2.3 // finger radius
	const sp = 4.3 // finger spacing
	wrist := around(0, -96, rg+11).Add(g.U.Mul(2.5))

	// forearm in a laced leather bracer with gold rims, leaving the frame at the bottom
	armT := tube{
		path: catmullChain([]V3{wrist, wrist.Add(V3{3, -12, 3}), wrist.Add(V3{6, -26, 5}), wrist.Add(V3{8, -42, 6})}, 14),
		edge: 0.85,
		radius: func(u float64) float64 {
			return 5.6 + 1.6*u
		},
		mat: func(u float64) []stop {
			switch {
			case u < 0.14:
				return skinRamp
			case u < 0.21 || (u > 0.66 && u < 0.73):
				return goldRamp
			default:
				return leatherRamp
			}
		},
		bright: func(u, s, a float64) float64 {
			if (u > 0.14 && u < 0.21) || (u > 0.66 && u < 0.73) {
				return 0.12 * math.Sin(2*math.Pi*(s/2.2+a*0.9)) // rope gold
			}
			if u > 0.21 && u < 0.66 {
				ph := s/3.2 - math.Floor(s/3.2)
				if math.Abs(a) < 0.12 && ph < 0.5 { // laces crossing the middle
					return 0.34
				}
				if math.Abs(math.Abs(a)-0.35) < 0.08 && ph > 0.4 && ph < 0.6 { // eyelets
					return 0.4
				}
			}
			return 0
		},
	}
	arm = armT.items()

	// back of the hand facing us: it rises from the wrist and wraps over the
	// front of the horn up to the knuckle row near the top
	span := 1.5*sp + rf
	const gu, gv = 10, 12
	shellR := rg + 2.4
	grid := make([][]V3, gv+1)
	for b := 0; b <= gv; b++ {
		v := float64(b) / gv
		grid[b] = make([]V3, gu+1)
		for a := 0; a <= gu; a++ {
			x := float64(a)/gu*2 - 1 // -1..1 across the hand
			var p V3
			if v < 0.35 { // from the wrist up to the underside of the horn
				k := v / 0.35
				lo := wrist.Add(tAx.Mul(x * 5.6))
				hi := around(x*span*0.92, -62, shellR+1.4*(1-x*x))
				p = lo.Add(hi.Sub(lo).Mul(k)).Add(g.U.Mul(1.6 * (1 - x*x) * math.Sin(math.Pi*k)))
			} else { // wrapping the horn
				k := (v - 0.35) / 0.65
				th := -62 + k*130
				bump := 1.4*(1-x*x) + 0.9*math.Abs(math.Sin(math.Pi*(x+1)*2))*smooth(0.8, 1, k) // knuckles
				round := 1 - 0.16*smooth(0.78, 1, k)*x*x - 0.06*(1-k)*x*x                       // rounded corners
				p = around(x*span*round, th, shellR+bump)
			}
			grid[b][a] = p
		}
	}
	for b := 0; b < gv; b++ {
		for a := 0; a < gu; a++ {
			q := []V3{grid[b][a], grid[b][a+1], grid[b+1][a+1], grid[b+1][a]}
			n := newell(q)
			if n.Dot(camDir) < 0 {
				n = n.Mul(-1)
			}
			c := ramp(skinRamp, lightness(n, 0.24, 0.6, 0.45, 22))
			back = append(back, item{depth(centroid(q)), polygon(q, c, c, 0.3), true})
		}
	}
	var outline []V3
	for b := 0; b <= gv; b++ {
		outline = append(outline, grid[b][0])
	}
	for a := 0; a <= gu; a++ {
		outline = append(outline, grid[gv][a])
	}
	for b := gv; b >= 0; b-- {
		outline = append(outline, grid[b][gu])
	}
	edge = polygon(outline, ink, ink, 1.6)

	// fingers: from the knuckle row they curl over the top of the horn and away
	for j := 0; j < 4; j++ {
		t := (float64(j) - 1.5) * sp
		var p []V3
		for th := 60.0; th <= 150; th += 10 {
			p = append(p, around(t, th, rg+rf*0.95))
		}
		rr := rf
		if j == 0 {
			rr = rf * 0.88 // little finger on the mouthpiece side
		}
		fingers = append(fingers, skinTube(catmullChain(p, 3), func(u float64) float64 {
			return rr * (1 + 0.1*math.Exp(-math.Pow((u-0.55)/0.12, 2)) - 0.1*u)
		}).items()...)
	}
	// thumb: from the side of the hand along the front of the horn, towards the bell
	tip := around(span+7, -9, rg+2.0)
	tx, ty := project(tip)
	thumbCap := item{-1e9, fmt.Sprintf(`<circle cx="%.2f" cy="%.2f" r="2.05" fill="%s" stroke="%s" stroke-width="1.4"/>
<circle cx="%.2f" cy="%.2f" r="2.05" fill="%s"/>`, tx, ty, ramp(skinRamp, 0.62), ink, tx, ty, ramp(skinRamp, 0.62)), true}
	thumb = skinTube(catmullChain([]V3{around(span-2, -58, rg+3.3), around(span+1.5, -38, rg+2.6), around(span+4.5, -20, rg+2.2), tip}, 6),
		func(u float64) float64 { return 2.8 - 0.75*u + 0.25*math.Exp(-math.Pow((u-0.45)/0.1, 2)) }).items()
	thumb = append([]item{thumbCap}, thumb...)
	return
}

func main() {
	name := flag.String("variant", "gold", "horn version: gold, ember or patina")
	flag.Parse()
	v, ok := variants[*name]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown variant %q: use gold, ember or patina\n", *name)
		os.Exit(2)
	}
	goldRamp, ivoryRamp, ink = v.gold, v.ivory, v.ink
	horn := tube{
		path:   hornPath(),
		radius: hornRadius,
		edge:   0.85,
		mat: func(u float64) []stop {
			if ok, _ := inBand(u); ok {
				return goldRamp
			}
			return ivoryRamp
		},
		bright: hornBright,
	}
	fr := horn.frames()
	arm, back, fingers, thumb, handEdge := handItems(fr)

	var body strings.Builder
	write := func(its []item) {
		for _, it := range sortGroups(its) {
			body.WriteString(it.svg + "\n")
		}
	}
	write(horn.items())
	body.WriteString(runes(fr))

	// the bell, turned towards the viewer: a thick gold lip and a deep throat
	end := fr[len(fr)-1]
	inner := func(k float64) frame {
		return frame{end.P.Sub(end.T.Mul(end.r * k * 0.05)).Add(right.Mul(end.r * k * 0.06)).Add(up.Mul(-end.r * k * 0.05)), end.T, end.S, end.U, end.r, 1, 0}
	}
	lip := ring(end, end.r+1.1, 72)
	in := ring(end, end.r*0.8, 72)
	body.WriteString(polygon(lip, ramp(goldRamp, 0.55), ink, 0.85) + "\n")
	var hl []V3 // lit upper-left of the lip
	for j := 22; j <= 50; j++ {
		hl = append(hl, lip[j%72])
	}
	for j := 50; j >= 22; j-- {
		hl = append(hl, in[j%72])
	}
	body.WriteString(polygon(hl, ramp(goldRamp, 0.86), "", 0) + "\n")
	for k, f := range []float64{0.8, 0.66, 0.52, 0.38} { // the throat narrowing into the dark
		body.WriteString(polygon(ring(inner(float64(k)), end.r*f, 64), ramp(throatRamp, 0.75-0.22*float64(k)), ink, 0.35) + "\n")
	}

	// mouthpiece cap
	st := fr[0]
	body.WriteString(polygon(ring(frame{st.P, st.T.Mul(-1), st.S, st.U, st.r, 0, 0}, st.r*1.2, 24), ramp(goldRamp, 0.72), ink, 0.7) + "\n")

	write(arm)
	write(fingers)
	body.WriteString(handEdge + "\n")
	write(back)
	write(thumb)

	svg := fmt.Sprintf(`<svg width="128" height="128" viewBox="0 0 128 128" xmlns="http://www.w3.org/2000/svg">
  <defs>
    <filter id="grain" x="0" y="0" width="128" height="128" filterUnits="userSpaceOnUse">
      <feTurbulence type="fractalNoise" baseFrequency="0.75" numOctaves="2" seed="6" result="noise"/>
      <feColorMatrix in="noise" type="matrix" result="tint"
        values="0 0 0 0 0.36
                0 0 0 0 0.27
                0 0 0 0 0.15
                0 0 0 0.12 0"/>
      <feComposite in="tint" in2="SourceAlpha" operator="in" result="g"/>
      <feMerge><feMergeNode in="SourceGraphic"/><feMergeNode in="g"/></feMerge>
    </filter>
  </defs>
  <g filter="url(#grain)" stroke-linejoin="round">
%s  </g>
</svg>
`, body.String())
	out := "gjallarhorn.svg"
	if flag.NArg() > 0 {
		out = flag.Arg(0)
	}
	if err := os.WriteFile(out, []byte(svg), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

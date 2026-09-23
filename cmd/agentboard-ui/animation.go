//go:build js && wasm

package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"syscall/js"
)

// fixitAnimation builds the small "agent at work" illustration for the
// AGENTBOARD-9 view-all popup: a friendly bot waving a wrench at a spinning
// gear. It is a handful of inline SVG shapes animated entirely by CSS
// (.fixit-* rules in style.css), never a fetched image or GIF - see
// CLAUDE.md invariant 1 (no CDN, no runtime downloads) and the task
// comment: this must be self-contained. Geometry (the gear's teeth) is
// computed with plain trigonometry rather than a baked-in path string, so
// the whole thing is a few hundred bytes of markup with no binary asset at
// all.
func fixitAnimation() js.Value {
	gear := svg("g", map[string]string{"class": "fixit-gear"},
		svg("polygon", map[string]string{"points": gearPoints(70, 34, 18, 12, 8), "class": "fixit-gear-body"}),
		svg("circle", map[string]string{"cx": "70", "cy": "34", "r": "6", "class": "fixit-gear-hub"}),
	)
	bot := svg("g", map[string]string{"class": "fixit-bot"},
		svg("circle", map[string]string{"cx": "34", "cy": "62", "r": "22", "class": "fixit-bot-body"}),
		svg("circle", map[string]string{"cx": "26", "cy": "57", "r": "3", "class": "fixit-bot-eye"}),
		svg("circle", map[string]string{"cx": "42", "cy": "57", "r": "3", "class": "fixit-bot-eye"}),
		svg("path", map[string]string{"d": "M24 70 Q34 76 44 70", "class": "fixit-bot-smile"}),
		svg("g", map[string]string{"class": "fixit-arm"},
			svg("rect", map[string]string{"x": "44", "y": "56", "width": "26", "height": "7", "rx": "3.5", "class": "fixit-wrench"}),
			svg("circle", map[string]string{"cx": "70", "cy": "59.5", "r": "6", "class": "fixit-wrench-head"}),
		),
	)
	return svg("svg", map[string]string{
		"viewBox": "0 0 100 100", "class": "fixit-anim", "aria-hidden": "true", "focusable": "false",
	}, gear, bot)
}

// gearPoints returns the SVG polygon "points" attribute for a simple
// teeth-toothed gear silhouette centered at (cx, cy): teeth alternating
// vertices at rOuter and rInner around the circle.
func gearPoints(cx, cy, rOuter, rInner float64, teeth int) string {
	n := teeth * 2
	pts := make([]string, n)
	for i := range n {
		angle := float64(i) * math.Pi / float64(teeth)
		r := rOuter
		if i%2 == 1 {
			r = rInner
		}
		x := cx + r*math.Cos(angle)
		y := cy + r*math.Sin(angle)
		pts[i] = fmt.Sprintf("%s,%s", trim1(x), trim1(y))
	}
	return strings.Join(pts, " ")
}

func trim1(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) }

//go:build js && wasm

package main

import (
	"errors"
	"strconv"
	"syscall/js"

	"github.com/JiaBao-do/agentboard/internal/view"
	"github.com/JiaBao-do/agentboard/model"
)

var (
	global = js.Global()
	doc    = global.Get("document")
)

// el creates an element. Children may be a string (added as a text node,
// never as HTML), a js.Value, a []js.Value or nil. Task text comes from
// untrusted agents, so nothing here ever uses innerHTML.
func el(tag, class string, kids ...any) js.Value {
	e := doc.Call("createElement", tag)
	if class != "" {
		e.Set("className", class)
	}
	for _, k := range kids {
		appendKid(e, k)
	}
	return e
}

func appendKid(e js.Value, k any) {
	switch v := k.(type) {
	case nil:
	case string:
		e.Call("appendChild", doc.Call("createTextNode", v))
	case js.Value:
		if !v.IsUndefined() && !v.IsNull() {
			e.Call("appendChild", v)
		}
	case []js.Value:
		for _, c := range v {
			appendKid(e, c)
		}
	}
}

// svgNS is the SVG namespace; SVG elements must be created with
// createElementNS, not createElement (el above), or the browser renders
// them as unknown HTML elements instead of graphics.
const svgNS = "http://www.w3.org/2000/svg"

// svg creates an SVG element (tag "svg", "circle", "path", "g", ...) with
// the given attributes, for the AGENTBOARD-9 working/fixing animation. Like
// el, it never touches innerHTML.
func svg(tag string, attrs map[string]string, kids ...js.Value) js.Value {
	e := doc.Call("createElementNS", svgNS, tag)
	for k, v := range attrs {
		e.Call("setAttribute", k, v)
	}
	for _, k := range kids {
		if !k.IsUndefined() && !k.IsNull() {
			e.Call("appendChild", k)
		}
	}
	return e
}

func attr(e js.Value, kv ...string) js.Value {
	for i := 0; i+1 < len(kv); i += 2 {
		e.Call("setAttribute", kv[i], kv[i+1])
	}
	return e
}

// act tags e so the delegated listener on the root dispatches it.
func act(e js.Value, name, id string) js.Value {
	attr(e, "data-action", name)
	if id != "" {
		attr(e, "data-id", id)
	}
	return e
}

// closest walks up from v to the nearest ancestor-or-self matching sel, or
// js.Null() if v is not an element (event targets can be the document or a
// text node) or no ancestor matches.
func closest(v js.Value, sel string) js.Value {
	if v.Type() != js.TypeObject || !v.Get("closest").Truthy() {
		return js.Null()
	}
	return v.Call("closest", sel)
}

func dataOf(e js.Value, key string) string {
	v := e.Get("dataset").Get(key)
	if v.Type() != js.TypeString {
		return ""
	}
	return v.String()
}

func selectEl(name string, opts [][2]string, cur string) js.Value {
	s := el("select", "")
	if name != "" {
		attr(s, "name", name)
	}
	for _, o := range opts {
		op := el("option", "", o[1])
		op.Set("value", o[0])
		s.Call("appendChild", op)
	}
	s.Set("value", cur)
	return s
}

func input(name, placeholder string) js.Value {
	i := el("input", "")
	attr(i, "name", name, "placeholder", placeholder, "autocomplete", "off")
	return i
}

// dateInput builds an HTML5 date input (native calendar picker, and its own
// "clear" affordance in every evergreen browser) for a Timeline field
// (AGENTBOARD-6). d.String() ("" for a nil/zero Date) is exactly the
// "YYYY-MM-DD" format the input element's value expects.
func dateInput(name string, d *model.Date) js.Value {
	i := el("input", "")
	attr(i, "name", name, "type", "date")
	if d != nil {
		i.Set("value", d.String())
	}
	return i
}

func field(label string, control js.Value) js.Value {
	return el("label", "", label, control)
}

// avatarEl renders an agent's per-agent avatar: a small colored badge with
// its initials (view.Initials, from name) on a background chosen by hashing
// kind (view.AvatarPalette), so agents that share a kind share a color while
// staying individually distinguishable by their initials. It is computed
// client-side from data already on the Agent record - no fetched image, no
// stored file, no external service (CLAUDE.md invariant 1) - and is cheap
// enough to build on every render (a hash plus a couple of string ops) since
// only a handful of agents are ever shown at once. It never replaces the
// existing online/offline dot: callers place this alongside that dot, not
// instead of it, so that signal stays visible on its own.
func avatarEl(kind, name string) js.Value {
	class := "avatar avatar-" + strconv.Itoa(view.AvatarPalette(kind))
	return attr(el("span", class, view.Initials(name)), "aria-hidden", "true")
}

// await blocks the calling goroutine until the promise settles. Never call
// it from a js.FuncOf callback: start a goroutine first.
func await(p js.Value) (js.Value, error) {
	type result struct {
		v   js.Value
		err error
	}
	ch := make(chan result, 1)
	ok := js.FuncOf(func(_ js.Value, args []js.Value) any {
		ch <- result{v: args[0]}
		return nil
	})
	fail := js.FuncOf(func(_ js.Value, args []js.Value) any {
		msg := "request failed"
		if len(args) > 0 && args[0].Type() == js.TypeObject {
			if m := args[0].Get("message"); m.Type() == js.TypeString {
				msg = m.String()
			}
		}
		ch <- result{err: errors.New(msg)}
		return nil
	})
	p.Call("then", ok, fail)
	r := <-ch
	ok.Release()
	fail.Release()
	return r.v, r.err
}

type draft struct {
	key, val string
	focus    bool
}

// captureDrafts remembers what the user typed into elements marked
// data-draft, so a live refresh does not wipe a half written comment.
func captureDrafts() []draft {
	list := doc.Call("querySelectorAll", "[data-draft]")
	active := doc.Get("activeElement")
	n := list.Get("length").Int()
	out := make([]draft, 0, n)
	for i := 0; i < n; i++ {
		e := list.Call("item", i)
		out = append(out, draft{
			key:   dataOf(e, "draft"),
			val:   e.Get("value").String(),
			focus: e.Equal(active),
		})
	}
	return out
}

func restoreDrafts(ds []draft) {
	for _, d := range ds {
		e := doc.Call("querySelector", `[data-draft="`+d.key+`"]`)
		if e.IsNull() {
			continue
		}
		e.Set("value", d.val)
		if d.focus {
			e.Call("focus")
		}
	}
}

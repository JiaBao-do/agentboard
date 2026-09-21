//go:build js && wasm

package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"syscall/js"
)

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return fmt.Sprintf("%d %s", e.status, e.msg) }

// api performs a JSON request against the server that served this page.
func (a *app) api(method, path string, body any) ([]byte, error) {
	opts := global.Get("Object").New()
	hdr := global.Get("Object").New()
	hdr.Set("Accept", "application/json")
	if a.token != "" {
		hdr.Set("Authorization", "Bearer "+a.token)
	}
	if path == "/api/admin/shutdown" {
		hdr.Set("X-Agentboard-Action", "shutdown")
	}
	opts.Set("method", method)
	opts.Set("cache", "no-store")
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		opts.Set("body", string(b))
		hdr.Set("Content-Type", "application/json")
	}
	opts.Set("headers", hdr)
	resp, err := await(global.Call("fetch", path, opts))
	if err != nil {
		return nil, err
	}
	txt, err := await(resp.Call("text"))
	if err != nil {
		return nil, err
	}
	data := []byte(txt.String())
	if status := resp.Get("status").Int(); status >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = "request failed"
		}
		return nil, &httpError{status: status, msg: e.Error}
	}
	return data, nil
}

func storageGet(key string) (v string) {
	defer func() { _ = recover() }() // storage can throw in private modes
	item := global.Get("sessionStorage").Call("getItem", key)
	if item.Type() == js.TypeString {
		return item.String()
	}
	return ""
}

func storageSet(key, val string) {
	defer func() { _ = recover() }()
	global.Get("sessionStorage").Call("setItem", key, val)
}

// tokenFromHash accepts "#token=..." so a link can carry the token once; it
// is then moved to sessionStorage and stripped from the address bar.
func tokenFromHash() string {
	h := global.Get("location").Get("hash").String()
	const p = "#token="
	if !strings.HasPrefix(h, p) {
		return ""
	}
	tok := global.Call("decodeURIComponent", h[len(p):]).String()
	loc := global.Get("location")
	global.Get("history").Call("replaceState", js.Null(), "", loc.Get("pathname").String()+loc.Get("search").String())
	return tok
}

// connect (re)opens the Server-Sent Events stream. Every event is only a
// hint to re-fetch /api/state.
func (a *app) connect() {
	if a.es.Truthy() {
		a.es.Call("close")
	}
	u := "/api/events"
	if a.token != "" {
		u += "?token=" + global.Call("encodeURIComponent", a.token).String()
	}
	es := global.Get("EventSource").New(u)
	hint := js.FuncOf(func(js.Value, []js.Value) any {
		a.poke()
		return nil
	})
	for _, n := range []string{"hello", "change", "tick"} {
		es.Call("addEventListener", n, hint)
	}
	es.Set("onopen", js.FuncOf(func(js.Value, []js.Value) any {
		a.live = true
		a.render()
		return nil
	}))
	es.Set("onerror", js.FuncOf(func(js.Value, []js.Value) any {
		a.live = false
		a.render()
		return nil
	}))
	a.es = es
}

//go:build js && wasm

// Command agentboard-ui is the agentboard web UI: Go compiled to
// WebAssembly. It renders the board with plain DOM calls, keeps itself fresh
// with Server-Sent Events, and talks to the JSON API of the server that
// served it.
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"syscall/js"
	"time"

	"github.com/JiaBao-do/agentboard/model"
)

const (
	tokenKey    = "agentboard.token"
	webActor    = "web"
	pollEvery   = 20 * time.Second
	recentLimit = 8
)

type app struct {
	root     js.Value
	es       js.Value
	token    string
	snap     model.Snapshot
	detail   *model.TaskDetail
	selected string
	project  string
	epic     string
	showNew  bool
	loaded   bool
	live     bool
	needAuth bool
	loadErr  string
	notice   string
	refresh  chan struct{}
}

func main() {
	a := &app{
		root:    doc.Call("getElementById", "app"),
		refresh: make(chan struct{}, 1),
	}
	a.token = tokenFromHash()
	if a.token != "" {
		storageSet(tokenKey, a.token)
	} else {
		a.token = storageGet(tokenKey)
	}
	a.bind()
	a.connect()
	go a.loop()
	a.poke()
	select {} // the page owns our lifetime
}

// poke asks the loop to reload; bursts of events collapse into one reload.
func (a *app) poke() {
	select {
	case a.refresh <- struct{}{}:
	default:
	}
}

func (a *app) loop() {
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		select {
		case <-a.refresh:
		case <-tick.C:
		}
		a.reload()
	}
}

func (a *app) reload() {
	data, err := a.api("GET", "/api/state", nil)
	if err != nil {
		a.loadErr = err.Error()
		var he *httpError
		if asHTTP(err, &he) && he.status == 401 {
			a.needAuth = true
		}
		a.render()
		return
	}
	var snap model.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		a.loadErr = "bad response: " + err.Error()
		a.render()
		return
	}
	a.snap, a.loaded, a.needAuth, a.loadErr = snap, true, false, ""
	if a.selected != "" {
		a.reloadDetail()
	}
	active := 0
	for _, t := range snap.Tasks {
		if t.Status == model.StatusInProgress {
			active++
		}
	}
	doc.Set("title", fmt.Sprintf("agentboard (%d in progress)", active))
	a.render()
}

func (a *app) reloadDetail() {
	data, err := a.api("GET", "/api/tasks/"+a.selected, nil)
	if err != nil {
		var he *httpError
		if asHTTP(err, &he) && he.status == 404 {
			a.selected, a.detail = "", nil
		}
		return
	}
	var d model.TaskDetail
	if json.Unmarshal(data, &d) == nil {
		a.detail = &d
	}
}

func asHTTP(err error, target **httpError) bool {
	he, ok := err.(*httpError)
	if ok {
		*target = he
	}
	return ok
}

// bind installs one delegated listener per event type on the root. Nodes are
// rebuilt on every render, so per-node listeners would leak.
func (a *app) bind() {
	for _, name := range []string{"click", "change", "submit"} {
		typ := name
		a.root.Call("addEventListener", typ, js.FuncOf(func(_ js.Value, args []js.Value) any {
			a.dispatch(typ, args[0])
			return nil
		}))
	}
}

// dispatch runs synchronously inside the JS event, so it only reads the DOM
// and then hands the network work to a goroutine.
func (a *app) dispatch(typ string, ev js.Value) {
	target := ev.Get("target")
	if target.Type() != js.TypeObject || !target.Get("closest").Truthy() {
		return
	}
	n := target.Call("closest", "[data-action]")
	if n.IsNull() {
		return
	}
	name, id := dataOf(n, "action"), dataOf(n, "id")
	switch typ + " " + name {
	case "click select":
		a.selected, a.detail = id, nil
		a.render()
		a.poke()
	case "click close":
		a.selected, a.detail = "", nil
		a.render()
	case "click new":
		a.showNew = !a.showNew
		a.render()
	case "click cancel-new":
		a.showNew = false
		a.render()
	case "change project":
		a.project = n.Get("value").String()
		a.render()
	case "change epic":
		a.epic = n.Get("value").String()
		a.render()
	case "change set-status":
		a.act("PATCH", "/api/tasks/"+id, map[string]any{"status": n.Get("value").String(), "actor": webActor}, nil)
	case "change set-priority":
		a.act("PATCH", "/api/tasks/"+id, map[string]any{"priority": n.Get("value").String(), "actor": webActor}, nil)
	case "submit create-task":
		ev.Call("preventDefault")
		f := formValues(n, "project", "title", "description", "priority", "labels")
		body := map[string]any{
			"project": f["project"], "title": f["title"], "description": f["description"],
			"priority": f["priority"], "actor": webActor,
			"labels": splitLabels(f["labels"]),
		}
		a.act("POST", "/api/tasks", body, func() { a.showNew = false })
	case "submit create-project":
		ev.Call("preventDefault")
		f := formValues(n, "key", "name")
		a.act("POST", "/api/projects", map[string]any{"key": strings.ToUpper(f["key"]), "name": f["name"], "actor": webActor}, nil)
	case "submit comment":
		ev.Call("preventDefault")
		f := formValues(n, "text")
		a.act("POST", "/api/tasks/"+id+"/comment", map[string]any{"text": f["text"], "actor": webActor}, func() {
			n.Call("reset")
		})
	case "submit login":
		ev.Call("preventDefault")
		f := formValues(n, "token")
		a.token = strings.TrimSpace(f["token"])
		storageSet(tokenKey, a.token)
		a.connect()
		a.poke()
	}
}

// act runs a mutating request off the event loop, then refreshes.
func (a *app) act(method, path string, body any, ok func()) {
	a.notice = ""
	go func() {
		if _, err := a.api(method, path, body); err != nil {
			a.notice = err.Error()
		} else if ok != nil {
			ok()
		}
		a.poke()
		a.render()
	}()
}

func formValues(form js.Value, names ...string) map[string]string {
	fd := global.Get("FormData").New(form)
	out := make(map[string]string, len(names))
	for _, n := range names {
		v := fd.Call("get", n)
		if v.Type() == js.TypeString {
			out[n] = v.String()
		}
	}
	return out
}

func splitLabels(s string) []string {
	out := []string{}
	for _, l := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		out = append(out, l)
	}
	return out
}

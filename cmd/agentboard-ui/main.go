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

	"github.com/JiaBao-do/agentboard/internal/view"
	"github.com/JiaBao-do/agentboard/model"
)

const (
	tokenKey    = "agentboard.token"
	userKey     = "agentboard.user"
	defaultUser = "user"
	pollEvery   = 20 * time.Second
	sidebarPeek = 5    // agents/activity rows shown before "view all"
	fullHistory = 1000 // limit passed to /api/activity when expanded
)

type app struct {
	root     js.Value
	es       js.Value
	token    string
	snap     model.Snapshot
	detail   *model.TaskDetail
	selected string
	filter   view.Filter
	showNew  bool
	loaded   bool
	stopped  bool
	live     bool
	needAuth bool
	loadErr  string
	notice   string
	dragging bool

	// Sidebar "view all" state (AGENTBOARD-4): agents are never capped by
	// the server, so showAllAgents just expands what is already in snap.
	// Activity is capped in the snapshot (see board.go, snapshotActivity),
	// so expanding it fetches the fuller list from /api/activity once and
	// caches it in allActivity until the next page load.
	showAllAgents   bool
	showAllActivity bool
	allActivity     []model.Activity

	refresh chan struct{}
}

func main() {
	a := &app{
		root:    doc.Call("getElementById", "app"),
		refresh: make(chan struct{}, 1),
	}
	a.filter = view.ParseFilterQuery(global.Get("location").Get("search").String())
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
	if a.stopped {
		return
	}
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

// loadAllActivity fetches the fuller activity history for the "view all"
// expansion of the Recent activity panel: the snapshot only carries the
// newest snapshotActivity entries (see board.go), so this uses the
// existing GET /api/activity?limit= endpoint instead of trimming what is
// already in memory. Cached in a.allActivity until the next page load.
func (a *app) loadAllActivity() {
	go func() {
		data, err := a.api("GET", fmt.Sprintf("/api/activity?limit=%d", fullHistory), nil)
		if err != nil {
			a.notice = err.Error()
			a.render()
			return
		}
		var acts []model.Activity
		if json.Unmarshal(data, &acts) == nil {
			a.allActivity = acts
		}
		a.render()
	}()
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
// user is the display name humans act under (never "anonymous").
func (a *app) user() string {
	if u := storageGet(userKey); u != "" {
		return u
	}
	return defaultUser
}

// setUser stores a display name, replacing characters the API would refuse.
func (a *app) setUser(name string) {
	var b []rune
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b = append(b, r)
		default:
			b = append(b, '-')
		}
	}
	if len(b) == 0 || !(b[0] >= 'a' && b[0] <= 'z' || b[0] >= 'A' && b[0] <= 'Z' || b[0] >= '0' && b[0] <= '9') {
		storageSet(userKey, "")
		return
	}
	if len(b) > 64 {
		b = b[:64]
	}
	storageSet(userKey, string(b))
}

func (a *app) bind() {
	for _, name := range []string{"click", "change", "submit", "input"} {
		typ := name
		a.root.Call("addEventListener", typ, js.FuncOf(func(_ js.Value, args []js.Value) any {
			a.dispatch(typ, args[0])
			return nil
		}))
	}
	// Drag-and-drop uses its own delegated listeners: dragover/drop must
	// find the enclosing column even when the pointer is over a card, which
	// the generic closest("[data-action]") lookup below would not do (a
	// card's own data-action="select" is a closer match than the column's).
	for _, name := range []string{"dragstart", "dragover", "drop", "dragend"} {
		typ := name
		a.root.Call("addEventListener", typ, js.FuncOf(func(_ js.Value, args []js.Value) any {
			a.dispatchDrag(typ, args[0])
			return nil
		}))
	}
}

// dispatch runs synchronously inside the JS event, so it only reads the DOM
// and then hands the network work to a goroutine.
func (a *app) dispatch(typ string, ev js.Value) {
	n := closest(ev.Get("target"), "[data-action]")
	if n.IsNull() {
		return
	}
	name, id := dataOf(n, "action"), dataOf(n, "id")
	switch typ + " " + name {
	case "click select":
		if a.dragging {
			return // a drag just ended on this card; do not also open it
		}
		a.selected, a.detail = id, nil
		a.render()
		a.poke()
	case "click close":
		a.selected, a.detail = "", nil
		a.render()
	case "click stop":
		msg := "Stop the server?\n\nEvery agent and this page lose their connection. " +
			"Your data is saved first. You must start it again from the command line."
		if global.Call("confirm", msg).Bool() {
			a.stopServer()
		}
	case "click new":
		a.showNew = !a.showNew
		a.render()
	case "click cancel-new":
		a.showNew = false
		a.render()
	case "change me":
		a.setUser(n.Get("value").String())
		a.render()
	case "change project":
		a.filter.Project = n.Get("value").String()
		a.filterChanged()
	case "change epic":
		a.filter.Epic = n.Get("value").String()
		a.filterChanged()
	case "input query":
		a.filter.Query = n.Get("value").String()
		a.filterChanged()
	case "change status-filter":
		a.filter.Status = model.Status(n.Get("value").String())
		a.filterChanged()
	case "change assignee-filter":
		a.filter.Assignee = n.Get("value").String()
		a.filterChanged()
	case "change type-filter":
		a.filter.Type = model.Kind(n.Get("value").String())
		a.filterChanged()
	case "change priority-filter":
		a.filter.Priority = model.Priority(n.Get("value").String())
		a.filterChanged()
	case "click clear-filters":
		a.filter = view.Filter{}
		a.filterChanged()
	case "click show-all-agents":
		a.showAllAgents = !a.showAllAgents
		a.render()
	case "click show-all-activity":
		a.showAllActivity = !a.showAllActivity
		if a.showAllActivity && a.allActivity == nil {
			a.loadAllActivity()
		}
		a.render()
	case "change set-status":
		a.act("PATCH", "/api/tasks/"+id, map[string]any{"status": n.Get("value").String(), "actor": a.user()}, nil)
	case "change set-priority":
		a.act("PATCH", "/api/tasks/"+id, map[string]any{"priority": n.Get("value").String(), "actor": a.user()}, nil)
	case "submit create-task":
		ev.Call("preventDefault")
		f := formValues(n, "project", "title", "description", "priority", "labels")
		body := map[string]any{
			"project": f["project"], "title": f["title"], "description": f["description"],
			"priority": f["priority"], "actor": a.user(),
			"labels": splitLabels(f["labels"]),
		}
		a.act("POST", "/api/tasks", body, func() { a.showNew = false })
	case "submit create-project":
		ev.Call("preventDefault")
		f := formValues(n, "key", "name")
		a.act("POST", "/api/projects", map[string]any{"key": strings.ToUpper(f["key"]), "name": f["name"], "actor": a.user()}, nil)
	case "submit comment":
		ev.Call("preventDefault")
		f := formValues(n, "text")
		a.act("POST", "/api/tasks/"+id+"/comment", map[string]any{"text": f["text"], "actor": a.user()}, func() {
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

// dispatchDrag implements HTML5 drag-and-drop between board columns. A drop
// calls the same PATCH /api/tasks/{id} endpoint the drawer's status
// dropdown uses (see "change set-status" above), so the server's lease and
// claim rules are the only ones ever enforced; the client makes no status
// decision of its own.
func (a *app) dispatchDrag(typ string, ev js.Value) {
	target := ev.Get("target")
	switch typ {
	case "dragstart":
		card := closest(target, "[draggable]")
		if card.IsNull() {
			return
		}
		id := dataOf(card, "id")
		if id == "" {
			return
		}
		a.dragging = true
		if dt := ev.Get("dataTransfer"); dt.Truthy() {
			dt.Call("setData", "text/plain", id)
			dt.Set("effectAllowed", "move")
		}
	case "dragover":
		zone := closest(target, "[data-drop-status]")
		if zone.IsNull() {
			return
		}
		ev.Call("preventDefault") // required to become a valid drop target
		clearDragOver()
		zone.Get("classList").Call("add", "drag-over")
	case "drop":
		clearDragOver()
		zone := closest(target, "[data-drop-status]")
		if zone.IsNull() {
			return
		}
		ev.Call("preventDefault")
		status := dataOf(zone, "dropStatus")
		dt := ev.Get("dataTransfer")
		if status == "" || !dt.Truthy() {
			return
		}
		id := dt.Call("getData", "text/plain").String()
		if id == "" {
			return
		}
		a.act("PATCH", "/api/tasks/"+id, map[string]any{"status": status, "actor": a.user()}, nil)
	case "dragend":
		a.dragging = false
		clearDragOver()
	}
}

// clearDragOver removes the hover highlight from every column; called
// before re-adding it to the current target and on drop/dragend so a stray
// column never keeps the highlight after the pointer leaves it.
func clearDragOver() {
	cols := doc.Call("querySelectorAll", ".column.drag-over")
	for i := range cols.Get("length").Int() {
		cols.Call("item", i).Get("classList").Call("remove", "drag-over")
	}
}

// filterChanged re-renders after a filter control changes and mirrors the
// filter into the URL query string (replacing history, not pushing a new
// entry) so a filtered view is shareable and survives a reload — the same
// history.replaceState pattern tokenFromHash uses to keep the address bar
// in sync (see api.go).
func (a *app) filterChanged() {
	loc := global.Get("location")
	u := loc.Get("pathname").String()
	if qs := view.FilterQuery(a.filter); qs != "" {
		u += "?" + qs
	}
	global.Get("history").Call("replaceState", js.Null(), "", u)
	a.render()
}

// stopServer asks the server to shut down, then shows the stopped screen and
// stops the live connection so the browser does not keep retrying.
func (a *app) stopServer() {
	go func() {
		if _, err := a.api("POST", "/api/admin/shutdown", map[string]any{}); err != nil {
			a.notice = "Could not stop the server: " + err.Error()
			a.render()
			return
		}
		a.stopped = true
		if a.es.Truthy() {
			a.es.Set("onerror", js.Null())
			a.es.Call("close")
		}
		a.render()
	}()
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

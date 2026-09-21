//go:build js && wasm

package main

import (
	"fmt"
	"syscall/js"

	"github.com/JiaBao-do/agentboard/internal/view"
	"github.com/JiaBao-do/agentboard/model"
)

func (a *app) render() {
	drafts := captureDrafts()
	a.root.Call("replaceChildren", a.page())
	restoreDrafts(drafts)
}

func (a *app) page() js.Value {
	if a.stopped {
		return a.stoppedView()
	}
	if a.needAuth {
		return a.authView()
	}
	if !a.loaded {
		msg := "Loading…"
		if a.loadErr != "" {
			msg = "Cannot reach the server: " + a.loadErr
		}
		return el("p", "boot", msg)
	}
	var banner js.Value
	if msg := firstNonEmpty(a.notice, a.loadErr); msg != "" {
		banner = el("div", "banner", msg)
	}
	var newPanel js.Value
	if a.showNew {
		newPanel = a.newTaskForm()
	}
	var drawer js.Value
	if a.selected != "" {
		drawer = a.drawer()
	}
	body := a.board()
	if len(a.snap.Projects) == 0 {
		body = a.firstProject()
	}
	return el("div", "app",
		a.topbar(), banner, newPanel,
		el("div", "layout", body, a.sidebar()),
		drawer,
	)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func (a *app) topbar() js.Value {
	opts := [][2]string{{"", "All projects"}}
	for _, p := range a.snap.Projects {
		opts = append(opts, [2]string{p.Key, p.Key + " · " + p.Name})
	}
	sel := act(selectEl("project", opts, a.project), "project", "")
	epicOpts := [][2]string{{"", "All epics"}}
	for _, e := range view.Epics(a.snap.Tasks) {
		epicOpts = append(epicOpts, [2]string{e.ID, e.ID + " · " + e.Title})
	}
	epicSel := act(selectEl("epic", epicOpts, a.epic), "epic", "")
	newBtn := act(el("button", "primary", "+ New task"), "new", "")
	attr(newBtn, "type", "button")

	dot, label := "dot", "reconnecting…"
	if a.live {
		dot, label = "dot on", "live"
	}
	save := a.snap.Save
	saveLabel := map[string]string{"saved": "Saved", "saving": "Saving…", "failed": "Save failed"}[save.State()]
	saveClass := "chip save-" + save.State()
	saveTitle := "mode: " + save.Mode
	if save.LastError != "" {
		saveTitle = save.LastError
	}
	saveChip := attr(el("span", saveClass, saveLabel), "title", saveTitle)
	me := input("me", "your name")
	me.Set("value", a.user())
	attr(me, "size", "10", "title", "the name your changes are recorded under")
	act(me, "me", "")
	return el("header", "topbar",
		el("span", "brand", "agentboard"),
		sel, epicSel, newBtn,
		el("span", "spacer"),
		el("label", "small muted", "you: ", me),
		attr(act(el("button", "", "Stop server"), "stop", ""), "type", "button", "title", "Save and shut the server down"),
		saveChip,
		el("span", "live", el("span", dot), label),
	)
}

func (a *app) firstProject() js.Value {
	form := act(el("form", "form",
		field("Key (2-10 letters or digits, e.g. AB)", input("key", "AB")),
		field("Name", input("name", "My project")),
		el("div", "row", attr(el("button", "primary", "Create project"), "type", "submit")),
	), "create-project", "")
	return el("div", "panel", el("h2", "", "Create your first project"),
		el("p", "muted", "Tasks live in projects; task IDs look like AB-1."), form)
}

func (a *app) newTaskForm() js.Value {
	var projects [][2]string
	for _, p := range a.snap.Projects {
		projects = append(projects, [2]string{p.Key, p.Key + " · " + p.Name})
	}
	cur := a.project
	if cur == "" && len(projects) > 0 {
		cur = projects[0][0]
	}
	var prios [][2]string
	for _, p := range model.Priorities {
		prios = append(prios, [2]string{string(p), string(p)})
	}
	title := input("title", "What needs doing?")
	attr(title, "required", "", "maxlength", "200", "data-draft", "new-title")
	desc := el("textarea", "")
	attr(desc, "name", "description", "data-draft", "new-desc", "placeholder", "Details (optional)")
	labels := input("labels", "bug, backend")
	attr(labels, "data-draft", "new-labels")
	proj := selectEl("project", projects, cur)
	prio := selectEl("priority", prios, string(model.PriorityMedium))
	cancel := act(el("button", "", "Cancel"), "cancel-new", "")
	attr(cancel, "type", "button")
	form := act(el("form", "form",
		field("Project", proj), field("Title", title), field("Description", desc),
		field("Priority", prio), field("Labels", labels),
		el("div", "row", attr(el("button", "primary", "Create task"), "type", "submit"), cancel),
	), "create-task", "")
	return el("div", "panel", el("h2", "", "New task"), form)
}

func (a *app) board() js.Value {
	cols := view.Columns(view.InEpic(a.snap.Tasks, a.epic), a.project)
	nodes := make([]js.Value, 0, len(cols))
	for _, c := range cols {
		cards := make([]js.Value, 0, len(c.Tasks))
		for _, t := range c.Tasks {
			cards = append(cards, a.card(t))
		}
		var empty js.Value
		if len(cards) == 0 {
			empty = el("div", "empty", "Nothing here")
		}
		nodes = append(nodes, el("section", "column",
			el("h2", "", el("span", "", c.Label), el("span", "", fmt.Sprint(len(c.Tasks)))),
			cards, empty))
	}
	return el("main", "board", nodes)
}

func (a *app) agentTag(name string) js.Value {
	if name == "" {
		return el("span", "muted small", "unassigned")
	}
	dot := "dot"
	if view.AgentOnline(a.snap.Agents, name) {
		dot = "dot on"
	}
	return el("span", "agent-tag", el("span", dot), name)
}

func (a *app) card(t model.Task) js.Value {
	var labels []js.Value
	for _, l := range t.Labels {
		labels = append(labels, el("span", "chip", l))
	}
	foot := []any{a.agentTag(t.Assignee)}
	if lease := view.LeaseLeft(a.snap.Now, t); lease != "" {
		foot = append(foot, el("span", "muted small", lease))
	}
	head := []any{el("span", "id", t.ID)}
	if t.Type != model.KindTask && t.Type != "" {
		head = append(head, el("span", "chip kind-"+string(t.Type), string(t.Type)))
	}
	head = append(head, el("span", "chip prio-"+string(t.Priority), string(t.Priority)))
	if t.Parent != "" {
		head = append(head, el("span", "muted small", "↑ "+t.Parent))
	}
	switch t.Type {
	case model.KindEpic:
		done, total := view.EpicProgress(a.snap.Tasks, t.ID)
		foot = append(foot, el("span", "muted small", fmt.Sprintf("%d/%d tasks done", done, total)))
	case model.KindStory:
		done, total := view.Progress(view.Children(a.snap.Tasks, t.ID))
		foot = append(foot, el("span", "muted small", fmt.Sprintf("%d/%d subtasks done", done, total)))
	}
	if t.UpdatedBy != "" {
		foot = append(foot, el("span", "muted small", "by "+t.UpdatedBy))
	}
	b := act(el("button", "card",
		el("div", "row", head...),
		el("div", "title", t.Title),
		el("div", "row", labels),
		el("div", "row", foot...),
	), "select", t.ID)
	return attr(b, "type", "button")
}

func (a *app) sidebar() js.Value {
	agents := make([]js.Value, 0, len(a.snap.Agents))
	for _, ag := range a.snap.Agents {
		dot := "dot"
		if ag.Online {
			dot = "dot on"
		}
		status := "idle"
		if ag.CurrentTask != "" {
			status = "working on " + ag.CurrentTask + " " + view.TaskTitle(a.snap.Tasks, ag.CurrentTask)
		}
		agents = append(agents, el("div", "agent",
			el("div", "name", el("span", dot), ag.Name, el("span", "chip", ag.Kind)),
			el("div", "small", status),
			el("div", "muted small", "seen "+view.RelTime(a.snap.Now, ag.LastHeartbeat)),
		))
	}
	if len(agents) == 0 {
		agents = append(agents, el("div", "empty", "No agents have reported in yet."))
	}
	recent := make([]js.Value, 0, recentLimit)
	for i, ev := range a.snap.Activity {
		if i == recentLimit {
			break
		}
		recent = append(recent, el("div", "agent",
			el("div", "small", a.actorTag(ev.Actor), " "+view.Describe(ev)+" "+ev.TaskID),
			el("div", "muted small", view.RelTime(a.snap.Now, ev.Time)),
		))
	}
	return el("aside", "side",
		el("h2", "", "Agents"), agents,
		el("h2", "", "Recent activity"), recent,
	)
}

func (a *app) drawer() js.Value {
	closeBtn := attr(act(el("button", "", "Close"), "close", ""), "type", "button")
	if a.detail == nil || a.detail.Task.ID != a.selected {
		return el("div", "drawer", el("div", "row", el("span", "spacer"), closeBtn), el("p", "muted", "Loading…"))
	}
	t := a.detail.Task
	var statuses [][2]string
	for _, s := range model.Statuses {
		statuses = append(statuses, [2]string{string(s), s.Label()})
	}
	var prios [][2]string
	for _, p := range model.Priorities {
		prios = append(prios, [2]string{string(p), string(p)})
	}
	statusSel := act(selectEl("", statuses, string(t.Status)), "set-status", t.ID)
	prioSel := act(selectEl("", prios, string(t.Priority)), "set-priority", t.ID)

	lease := view.LeaseLeft(a.snap.Now, t)
	if lease == "" {
		lease = "none"
	}
	var labels []js.Value
	for _, l := range t.Labels {
		labels = append(labels, el("span", "chip", l))
	}
	if len(labels) == 0 {
		labels = append(labels, el("span", "muted", "none"))
	}
	var desc js.Value
	if t.Description != "" {
		desc = el("div", "desc", t.Description)
	}

	items := make([]js.Value, 0, len(a.detail.Activity))
	for i := len(a.detail.Activity) - 1; i >= 0; i-- { // newest first
		e := a.detail.Activity[i]
		li := el("li", "",
			el("div", "", a.actorTag(e.Actor), " "+view.Describe(e)),
			el("div", "muted small", view.RelTime(a.snap.Now, e.Time)),
		)
		if e.Action == "comment" {
			li.Call("appendChild", el("blockquote", "", e.Detail))
		}
		items = append(items, li)
	}

	text := el("textarea", "")
	attr(text, "name", "text", "placeholder", "Add a comment", "required", "", "maxlength", "5000",
		"data-draft", "comment-"+t.ID)
	comment := act(el("form", "form", text,
		el("div", "row", attr(el("button", "primary", "Comment"), "type", "submit")),
	), "comment", t.ID)

	return el("div", "drawer",
		el("div", "row", el("span", "chip", t.ID), el("span", "spacer"), closeBtn),
		el("h1", "", t.Title),
		desc,
		el("div", "fields",
			el("span", "muted", "Status"), statusSel,
			el("span", "muted", "Priority"), prioSel,
			el("span", "muted", "Type"), el("span", "", string(t.Type)),
			a.parentRow(t),
			el("span", "muted", "Agent"), a.agentTag(t.Assignee),
			el("span", "muted", "Lease"), el("span", "", lease),
			el("span", "muted", "Labels"), el("span", "row", labels),
			el("span", "muted", "Created by"), el("span", "row", a.actorTag(t.CreatedBy), el("span", "muted small", view.RelTime(a.snap.Now, t.CreatedAt))),
			el("span", "muted", "Updated by"), el("span", "row", a.actorTag(t.UpdatedBy), el("span", "muted small", view.RelTime(a.snap.Now, t.UpdatedAt))),
		),
		a.childrenSection(t),
		el("h2", "muted small", "Timeline"),
		el("ul", "timeline", items),
		comment,
	)
}

func (a *app) authView() js.Value {
	tok := input("token", "access token")
	attr(tok, "type", "password", "required", "", "autofocus", "")
	form := act(el("form", "form",
		field("This board needs an access token", tok),
		el("div", "row", attr(el("button", "primary", "Sign in"), "type", "submit")),
	), "login", "")
	var msg js.Value
	if a.token != "" {
		msg = el("p", "muted", "That token was rejected.")
	}
	return el("div", "panel", el("h2", "", "agentboard"), msg, form)
}

// parentRow links to the parent task, if any.
func (a *app) parentRow(t model.Task) js.Value {
	frag := doc.Call("createDocumentFragment")
	if t.Parent == "" {
		return frag
	}
	link := attr(act(el("button", "link", t.Parent+" · "+view.TaskTitle(a.snap.Tasks, t.Parent)), "select", t.Parent), "type", "button")
	frag.Call("appendChild", el("span", "muted", "Parent"))
	frag.Call("appendChild", link)
	return frag
}

// childrenSection lists a task's direct children with their status.
func (a *app) childrenSection(t model.Task) js.Value {
	kids := view.Children(a.snap.Tasks, t.ID)
	if len(kids) == 0 {
		return js.Undefined()
	}
	done, total := view.Progress(kids)
	items := make([]js.Value, 0, len(kids))
	for _, k := range kids {
		link := attr(act(el("button", "link", k.ID+" · "+k.Title), "select", k.ID), "type", "button")
		items = append(items, el("li", "", el("span", "chip", k.Status.Label()), " ", link))
	}
	return el("div", "children",
		el("h2", "muted small", fmt.Sprintf("Children (%d/%d done)", done, total)),
		el("ul", "childlist", items),
	)
}

// actorTag shows who did something: a live/finished dot for agents, plus
// what the agent is working on. People ("user") and "system" get no dot.
func (a *app) actorTag(name string) js.Value {
	s := view.ActorOf(a.snap.Agents, name)
	dot := el("span", "")
	if s.Agent {
		cls := "dot"
		if s.Online {
			cls = "dot on"
		}
		dot = el("span", cls)
	}
	tag := el("span", "agent-tag", dot, name)
	if txt := view.ActorText(s); txt != "" {
		tag.Call("appendChild", el("span", "muted small", " · "+txt))
	}
	return tag
}

// stoppedView replaces the board once the server has been stopped.
func (a *app) stoppedView() js.Value {
	cmd := view.RestartCommand(global.Get("location").Get("host").String())
	return el("div", "panel",
		el("h2", "", "Server stopped"),
		el("p", "muted", "Your data was saved. Start the server again from a terminal, then reload this page:"),
		el("pre", "cmd", cmd),
		attr(act(el("button", "primary", "Copy command"), "copy-restart", ""), "type", "button"),
	)
}

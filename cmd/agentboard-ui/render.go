//go:build js && wasm

package main

import (
	"fmt"
	"strconv"
	"syscall/js"
	"time"

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
	var accountPanel js.Value
	if a.showAccount {
		accountPanel = a.accountView()
	}
	var drawer js.Value
	if a.selected != "" {
		drawer = a.drawer()
	}
	var filterbar js.Value
	var body js.Value
	switch {
	case len(a.snap.Projects) == 0:
		body = a.firstProject()
	case a.tab == "timeline":
		body = a.timelineView()
	default:
		body, filterbar = a.board(), a.filterbar()
	}
	var modal js.Value
	if a.viewAllKind != "" {
		modal = a.viewAllModal()
	}
	return el("div", "app",
		a.topbar(), filterbar, banner, newPanel, accountPanel,
		el("div", "layout", body, a.sidebar()),
		drawer, modal,
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
	sel := act(selectEl("project", opts, a.filter.Project), "project", "")
	epicOpts := [][2]string{{"", "All epics"}}
	for _, e := range view.Epics(a.snap.Tasks) {
		epicOpts = append(epicOpts, [2]string{e.ID, e.ID + " · " + e.Title})
	}
	epicSel := act(selectEl("epic", epicOpts, a.filter.Epic), "epic", "")
	newBtn := act(el("button", "primary", "+ New task"), "new", "")
	attr(newBtn, "type", "button")
	tabs := a.viewTabs()

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
	var boardControls js.Value
	if a.tab != "timeline" {
		boardControls = el("span", "row", sel, epicSel, newBtn)
	}
	return el("header", "topbar",
		el("span", "brand", "agentboard"),
		tabs, boardControls,
		el("span", "spacer"),
		a.identity(),
		attr(act(el("button", "", "Stop server"), "stop", ""), "type", "button", "title", "Save and shut the server down"),
		saveChip,
		el("span", "live", el("span", dot), label),
	)
}

// viewTabs switches the main panel between the Kanban board and the
// AGENTBOARD-6 Timeline (Gantt) view; the sidebar and topbar identity stay
// put either way.
func (a *app) viewTabs() js.Value {
	board := attr(act(el("button", tabClass(a.tab != "timeline"), "Board"), "tab-board", ""), "type", "button")
	timeline := attr(act(el("button", tabClass(a.tab == "timeline"), "Timeline"), "tab-timeline", ""), "type", "button")
	return el("span", "tabs", board, timeline)
}

// identity shows either the logged-in account (with a Log out button) or
// the existing free-typed name box plus a Log in link. Logging in is
// entirely optional: nothing here is required to use the board (see
// app.account's doc comment and docs/PITFALLS.md).
func (a *app) identity() js.Value {
	if a.account != "" {
		logout := attr(act(el("button", "link small", "Log out"), "account-logout", ""), "type", "button")
		return el("span", "small muted", "signed in as ", el("strong", "", a.account), " ", logout)
	}
	me := input("me", "your name")
	me.Set("value", a.user())
	attr(me, "size", "10", "title", "the name your changes are recorded under")
	act(me, "me", "")
	login := attr(act(el("button", "link small", "Log in"), "show-account", ""), "type", "button")
	return el("span", "small muted", "you: ", me, " ", login)
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
	cur := a.filter.Project
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
	tasks := view.FilterTasks(view.InEpic(a.snap.Tasks, a.filter.Epic), a.filter)
	cols := view.Columns(tasks, a.filter.Project)
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
		// data-drop-status makes the whole column a drag-and-drop target;
		// dropping a card here PATCHes its status (see dispatchDrag).
		col := el("section", "column",
			el("h2", "", el("span", "", c.Label), el("span", "", fmt.Sprint(len(c.Tasks)))),
			cards, empty)
		attr(col, "data-drop-status", string(c.Status))
		nodes = append(nodes, col)
	}
	return el("main", "board", nodes)
}

// filterbar is the combinable filter row: free-text search plus project,
// status, assignee, type and priority narrowing. All fields live in
// a.filter and are mirrored into the URL query string by filterChanged.
func (a *app) filterbar() js.Value {
	q := input("query", "Search title, description, labels…")
	attr(q, "type", "search", "data-draft", "filter-query")
	q.Set("value", a.filter.Query)
	act(q, "query", "")

	statusOpts := [][2]string{{"", "Any status"}}
	for _, s := range model.Statuses {
		statusOpts = append(statusOpts, [2]string{string(s), s.Label()})
	}
	statusSel := act(selectEl("", statusOpts, string(a.filter.Status)), "status-filter", "")

	assigneeOpts := [][2]string{{"", "Any assignee"}, {view.Unassigned, "Unassigned"}}
	for _, name := range view.Assignees(a.snap.Tasks) {
		assigneeOpts = append(assigneeOpts, [2]string{name, name})
	}
	assigneeSel := act(selectEl("", assigneeOpts, a.filter.Assignee), "assignee-filter", "")

	typeOpts := [][2]string{{"", "Any type"}}
	for _, k := range model.Kinds {
		typeOpts = append(typeOpts, [2]string{string(k), string(k)})
	}
	typeSel := act(selectEl("", typeOpts, string(a.filter.Type)), "type-filter", "")

	prioOpts := [][2]string{{"", "Any priority"}}
	for _, p := range model.Priorities {
		prioOpts = append(prioOpts, [2]string{string(p), string(p)})
	}
	prioSel := act(selectEl("", prioOpts, string(a.filter.Priority)), "priority-filter", "")

	var clear js.Value
	if !a.filter.Empty() {
		clear = attr(act(el("button", "", "Clear filters"), "clear-filters", ""), "type", "button")
	}
	return el("div", "filterbar", q, statusSel, assigneeSel, typeSel, prioSel, clear)
}

func (a *app) agentTag(name string) js.Value {
	if name == "" {
		return el("span", "muted small", "unassigned")
	}
	dot := "dot"
	if view.AgentOnline(a.snap.Agents, name) {
		dot = "dot on"
	}
	kind := view.AgentKind(a.snap.Agents, name)
	return el("span", "agent-tag", avatarEl(kind, name), el("span", dot), name)
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
	// draggable + data-id (from act above) let dispatchDrag move the task
	// between columns; the status dropdown in the drawer is the
	// keyboard/touch fallback for changing status without a mouse drag.
	return attr(b, "type", "button", "draggable", "true")
}

// sidebar shows two panels: the agents that have reported in, most
// recently active first, and the board's recent activity. Each panel peeks
// at sidebarPeek rows; "view all" (AGENTBOARD-4) opens the popup built by
// viewAllModal (AGENTBOARD-9) with the same row rendering, so the peeked
// and full lists always look identical.
func (a *app) sidebar() js.Value {
	sorted := view.SortAgentsByRecency(a.snap.Agents)
	agents := a.agentRows(view.Limit(sorted, sidebarPeek))
	if len(agents) == 0 {
		agents = []js.Value{el("div", "empty", "No agents have reported in yet.")}
	}

	activity := view.Limit(a.snap.Activity, sidebarPeek)
	moreActivity := len(a.snap.Activity) > sidebarPeek
	recent := a.activityRows(activity)
	if len(recent) == 0 {
		recent = []js.Value{el("div", "empty", "No activity yet.")}
	}

	return el("aside", "side",
		sideHead("Agents", len(sorted) > sidebarPeek, fmt.Sprintf("View all %d", len(sorted)), "show-all-agents"),
		el("div", "side-list", agents),
		sideHead("Recent activity", moreActivity, "View all", "show-all-activity"),
		el("div", "side-list", recent),
	)
}

// sideHead renders a sidebar section title, adding a "view all" control
// (which opens the AGENTBOARD-9 popup) only when there is something to
// view beyond the peeked rows already shown.
func sideHead(title string, showToggle bool, label, action string) js.Value {
	head := el("h2", "", title)
	if !showToggle {
		return el("div", "side-head", head)
	}
	toggle := attr(act(el("button", "link small", label), action, ""), "type", "button")
	return el("div", "side-head", head, toggle)
}

// agentRows renders one row per agent, shared by the sidebar peek and the
// AGENTBOARD-9 "view all" popup so both look identical.
func (a *app) agentRows(agents []model.Agent) []js.Value {
	out := make([]js.Value, 0, len(agents))
	for _, ag := range agents {
		dot := "dot"
		if ag.Online {
			dot = "dot on"
		}
		status := "idle"
		if ag.CurrentTask != "" {
			status = "working on " + ag.CurrentTask + " " + view.TaskTitle(a.snap.Tasks, ag.CurrentTask)
		}
		out = append(out, el("div", "agent",
			el("div", "name", avatarEl(ag.Kind, ag.Name), el("span", dot), ag.Name, el("span", "chip", ag.Kind)),
			el("div", "small", status),
			el("div", "muted small", "seen "+view.RelTime(a.snap.Now, ag.LastHeartbeat)),
		))
	}
	return out
}

// activityRows renders one row per activity entry, shared by the sidebar
// peek and the AGENTBOARD-9 "view all" popup. Each row is two lines (who
// did what, then a task chip and a dimmed relative time) rather than one
// flex row split between a long description and a timestamp: the old
// single-line layout let a long description push the timestamp around and
// mashed the task ID straight onto the sentence text (AGENTBOARD-10).
func (a *app) activityRows(activity []model.Activity) []js.Value {
	out := make([]js.Value, 0, len(activity))
	for _, ev := range activity {
		ts := attr(el("span", "activity-time", view.RelTime(a.snap.Now, ev.Time)), "title", ev.Time.Format(time.RFC1123))
		var meta js.Value
		if ev.TaskID != "" {
			meta = el("div", "activity-meta", el("span", "chip small", ev.TaskID), ts)
		} else {
			meta = el("div", "activity-meta", el("span", ""), ts)
		}
		out = append(out, el("div", "activity-row",
			el("div", "activity-main", a.actorTag(ev.Actor), el("span", "activity-desc", view.Describe(ev))),
			meta,
		))
	}
	return out
}

// viewAllModal is the AGENTBOARD-9 popup: the full agents or activity list
// (whichever "view all" was clicked, see a.viewAllKind) next to a small
// working/fixing animation, closeable via the button or Escape (see bind).
func (a *app) viewAllModal() js.Value {
	var title string
	var list []js.Value
	switch a.viewAllKind {
	case "agents":
		sorted := view.SortAgentsByRecency(a.snap.Agents)
		title = fmt.Sprintf("All agents (%d)", len(sorted))
		list = a.agentRows(sorted)
	case "activity":
		items := a.snap.Activity
		if a.allActivity != nil {
			items = a.allActivity
		}
		title = fmt.Sprintf("Recent activity (%d)", len(items))
		list = a.activityRows(items)
	default:
		return js.Undefined()
	}
	if len(list) == 0 {
		list = []js.Value{el("div", "empty", "Nothing here yet.")}
	}
	closeBtn := attr(act(el("button", "", "Close"), "close-view-all", ""), "type", "button")
	anim := el("div", "modal-anim", fixitAnimation(), el("p", "muted small", "hard at work…"))
	dialog := el("div", "modal-dialog",
		el("div", "row", el("h2", "", title), el("span", "spacer"), closeBtn),
		el("div", "modal-body", anim, el("div", "side-list modal-list", list)),
	)
	attr(dialog, "role", "dialog", "aria-modal", "true", "aria-label", title)
	return el("div", "modal-overlay", dialog)
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
	startInput := dateInput("start-date", t.StartDate)
	act(startInput, "set-start-date", t.ID)
	endInput := dateInput("end-date", t.EndDate)
	act(endInput, "set-end-date", t.ID)

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
			el("span", "muted", "Start date"), startInput,
			el("span", "muted", "End date"), endInput,
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

// accountView is the register/log in panel (AGENTBOARD-8). It is entirely
// separate from authView above: authView is the server's access token
// (ServerOptions.Token, shared by everyone with the token), while this is a
// per-person account (email + password). A board can use either, both or
// neither.
func (a *app) accountView() js.Value {
	tab := a.accountTab
	if tab == "" {
		tab = "login"
	}
	loginTab := attr(act(el("button", tabClass(tab == "login"), "Log in"), "account-tab-login", ""), "type", "button")
	registerTab := attr(act(el("button", tabClass(tab == "register"), "Register"), "account-tab-register", ""), "type", "button")
	closeBtn := attr(act(el("button", "", "Close"), "close-account", ""), "type", "button")

	email := input("email", "you@example.com")
	attr(email, "type", "email", "required", "", "autofocus", "", "autocapitalize", "off")
	pw := input("password", "password")
	attr(pw, "type", "password", "required", "", "minlength", "8",
		"autocomplete", map[bool]string{true: "new-password", false: "current-password"}[tab == "register"])

	action, submitLabel := "account-login", "Log in"
	if tab == "register" {
		action, submitLabel = "account-register", "Register (8+ character password)"
	}
	form := act(el("form", "form",
		field("Email", email), field("Password", pw),
		el("div", "row", attr(el("button", "primary", submitLabel), "type", "submit")),
	), action, "")

	var errMsg js.Value
	if a.accountErr != "" {
		errMsg = el("p", "banner", a.accountErr)
	}
	return el("div", "panel",
		el("div", "row", el("h2", "", "Account"), el("span", "spacer"), closeBtn),
		el("div", "row", loginTab, registerTab),
		errMsg, form,
	)
}

func tabClass(active bool) string {
	if active {
		return "primary"
	}
	return ""
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

// actorTag shows who did something: a per-agent avatar (color by kind,
// initials from name; see avatarEl), a live/finished dot for agents, plus
// what the agent is working on. People ("user") and "system" get no dot, but
// still get an avatar (empty kind, so a consistent neutral color) since the
// avatar is a generic identity badge, not agent-only chrome.
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
	tag := el("span", "agent-tag", avatarEl(s.Kind, name), dot, name)
	if txt := view.ActorText(s); txt != "" {
		tag.Call("appendChild", el("span", "muted small", " · "+txt))
	}
	return tag
}

// timelineView is the AGENTBOARD-6 Gantt/roadmap view: one project's dated
// tasks as horizontal bars under a month header with weekly gridlines. It
// replaces the Kanban board (see page()) but keeps the same sidebar.
func (a *app) timelineView() js.Value {
	a.ensureTimelineWindow()

	proj := a.timelineProject
	if proj == "" {
		proj = a.filter.Project
	}
	if proj == "" && len(a.snap.Projects) > 0 {
		proj = a.snap.Projects[0].Key
	}

	var projOpts [][2]string
	for _, p := range a.snap.Projects {
		projOpts = append(projOpts, [2]string{p.Key, p.Key + " · " + p.Name})
	}
	projSel := act(selectEl("timeline-project", projOpts, proj), "timeline-project", "")
	prevBtn := attr(act(el("button", "", "‹ Prev"), "timeline-prev", ""), "type", "button", "title", "One month earlier")
	todayBtn := attr(act(el("button", "", "Today"), "timeline-today", ""), "type", "button")
	nextBtn := attr(act(el("button", "", "Next ›"), "timeline-next", ""), "type", "button", "title", "One month later")
	windowLabel := el("span", "timeline-window-label", view.WindowLabel(a.timelineStart, a.timelineEnd))
	toolbar := el("div", "timeline-toolbar", projSel, prevBtn, todayBtn, nextBtn, windowLabel)

	if proj == "" {
		return el("main", "timeline", toolbar, el("div", "empty", "Create a project to use the Timeline view."))
	}

	var projectTasks []model.Task
	for _, t := range a.snap.Tasks {
		if t.Project == proj {
			projectTasks = append(projectTasks, t)
		}
	}
	dated := view.SortByStartDate(projectTasks)
	months := view.MonthsInWindow(a.timelineStart, a.timelineEnd)
	rows := view.TimelineRows(dated, a.timelineStart, a.timelineEnd)

	var note js.Value
	undated, omitted := len(projectTasks)-len(dated), len(dated)-len(rows)
	if undated > 0 || omitted > 0 {
		note = el("p", "muted small", fmt.Sprintf(
			"%d task(s) without both a start and end date, and %d outside this window, are not shown.", undated, omitted))
	}

	return el("main", "timeline", toolbar, a.timelineChart(months, rows), note)
}

// timelineChart lays out the month header (with weekly gridlines) and one
// row per dated task as a CSS grid: column 1 is a fixed-width label, column
// 2 is the proportional date track shared by the header, the gridline
// overlay and every bar, so they all line up exactly without JS layout
// math beyond the day-fraction arithmetic already done in internal/view.
func (a *app) timelineChart(months []view.Month, rows []view.TimelineRow) js.Value {
	totalDays := view.DaysBetween(a.timelineStart, a.timelineEnd)
	kids := make([]js.Value, 0, 4+3*len(rows))

	spacer := el("div", "timeline-spacer")
	attr(spacer, "style", "grid-column:1;grid-row:1")
	kids = append(kids, spacer)

	monthEls := make([]js.Value, 0, len(months))
	var tickOffsets []float64 // week ticks as a fraction of the whole window, for the gridline overlay
	for _, m := range months {
		monthDays := view.DaysBetween(m.Start, m.End)
		var ticks []js.Value
		for _, wt := range m.WeekTicks {
			tick := el("span", "timeline-month-tick", wt.Format("2"))
			attr(tick, "style", fmt.Sprintf("left:%s%%", trimPct(pct(view.DaysBetween(m.Start, wt), monthDays))))
			ticks = append(ticks, tick)
			tickOffsets = append(tickOffsets, pct(view.DaysBetween(a.timelineStart, wt), totalDays))
		}
		monthEl := el("div", "timeline-month",
			el("div", "timeline-month-label", m.Label),
			el("div", "timeline-month-ticks", ticks),
		)
		attr(monthEl, "style", fmt.Sprintf("width:%s%%", trimPct(pct(monthDays, totalDays))))
		monthEls = append(monthEls, monthEl)
	}
	header := el("div", "timeline-months", monthEls)
	attr(header, "style", "grid-column:2;grid-row:1")
	kids = append(kids, header)

	rowSpan := len(rows)
	if rowSpan == 0 {
		rowSpan = 1
	}
	gridTicks := make([]js.Value, 0, len(tickOffsets))
	for _, off := range tickOffsets {
		line := el("span", "timeline-gridline")
		attr(line, "style", fmt.Sprintf("left:%s%%", trimPct(off)))
		gridTicks = append(gridTicks, line)
	}
	gridlines := el("div", "timeline-gridlines", gridTicks)
	attr(gridlines, "style", fmt.Sprintf("grid-column:2;grid-row:2 / span %d", rowSpan))
	kids = append(kids, gridlines)

	if len(rows) == 0 {
		empty := el("div", "empty", "No dated tasks in this window.")
		attr(empty, "style", "grid-column:1 / -1;grid-row:2")
		kids = append(kids, empty)
	}
	for i, r := range rows {
		gridRow := i + 2
		label := el("div", "timeline-row-label", el("span", "id", r.Task.ID), " "+r.Task.Title)
		attr(label, "style", fmt.Sprintf("grid-column:1;grid-row:%d", gridRow))
		kids = append(kids, label)

		bar := act(el("button", "timeline-bar", r.Task.Title), "select", r.Task.ID)
		attr(bar, "type", "button",
			"style", fmt.Sprintf("left:%s%%;width:%s%%", trimPct(r.Left*100), trimPct(r.Width*100)),
			"title", fmt.Sprintf("%s · %s (%s → %s)", r.Task.ID, r.Task.Title, r.Task.StartDate, r.Task.EndDate))
		track := el("div", "timeline-track", bar)
		attr(track, "style", fmt.Sprintf("grid-column:2;grid-row:%d", gridRow))
		kids = append(kids, track)
	}

	return el("div", "timeline-chart", kids)
}

// pct returns part/total as a percentage, 0 for a non-positive total.
func pct(part, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}

func trimPct(f float64) string { return strconv.FormatFloat(f, 'f', 4, 64) }

// stoppedView replaces the board once the server has been stopped. It does
// not guess a restart command: a static page served by the now-stopped
// server has no reliable way to know whether this instance was started as
// a raw binary, "go run .", a pm2 process or a systemd service, so any
// single hardcoded command line would be right for some deployments and
// wrong (unusable if copy-pasted) for others.
func (a *app) stoppedView() js.Value {
	return el("div", "panel",
		el("h2", "", "Server stopped"),
		el("p", "muted", "Your data was saved. Start it again the way you originally started it "+
			"(see the README's \"Install and run\" section, or your process manager's config if you used one), "+
			"then reload this page."),
	)
}

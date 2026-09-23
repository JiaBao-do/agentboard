// Package view holds the UI's pure presentation logic (grouping, sorting,
// time formatting) so it can be unit tested natively instead of only inside
// a browser.
package view

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"time"

	"github.com/JiaBao-do/agentboard/model"
)

// Column is one board column.
type Column struct {
	Status model.Status
	Label  string
	Tasks  []model.Task
}

// Columns groups tasks into one column per status, in board order. Only
// tasks of project are included when project is not empty. Inside a column
// the most urgent task comes first, then the most recently updated.
func Columns(tasks []model.Task, project string) []Column {
	cols := make([]Column, len(model.Statuses))
	idx := map[model.Status]int{}
	for i, s := range model.Statuses {
		cols[i] = Column{Status: s, Label: s.Label(), Tasks: []model.Task{}}
		idx[s] = i
	}
	for _, t := range tasks {
		if project != "" && t.Project != project {
			continue
		}
		if i, ok := idx[t.Status]; ok {
			cols[i].Tasks = append(cols[i].Tasks, t)
		}
	}
	for i := range cols {
		ts := cols[i].Tasks
		sort.SliceStable(ts, func(a, b int) bool {
			if ra, rb := ts[a].Priority.Rank(), ts[b].Priority.Rank(); ra != rb {
				return ra > rb
			}
			return ts[a].UpdatedAt.After(ts[b].UpdatedAt)
		})
	}
	return cols
}

// RelTime formats t relative to now: "just now", "5m ago", "3h ago", "2d ago".
// Times after now read "in 5m".
func RelTime(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	if d < 0 {
		return "in " + short(-d)
	}
	if d < 10*time.Second {
		return "just now"
	}
	return short(d) + " ago"
}

func short(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}

// LeaseLeft describes the remaining lease of an in-progress task, or "" if
// the task holds no lease.
func LeaseLeft(now time.Time, t model.Task) string {
	if t.LeaseExpires == nil {
		return ""
	}
	d := t.LeaseExpires.Sub(now)
	if d <= 0 {
		return "lease expired"
	}
	return short(d) + " left"
}

// AgentOnline reports whether the named agent is online in agents.
func AgentOnline(agents []model.Agent, name string) bool {
	for _, a := range agents {
		if a.Name == name {
			return a.Online
		}
	}
	return false
}

// AgentKind returns the kind of the named agent, or "" if no agent by that
// name is registered. Used to color an agent's avatar (AvatarPalette)
// consistently for every place its identity is shown, even where only a
// bare name is on hand (e.g. a task's Assignee field).
func AgentKind(agents []model.Agent, name string) string {
	for _, a := range agents {
		if a.Name == name {
			return a.Kind
		}
	}
	return ""
}

// TaskTitle returns the title of the task with the given id, or "".
func TaskTitle(tasks []model.Task, id string) string {
	for _, t := range tasks {
		if t.ID == id {
			return t.Title
		}
	}
	return ""
}

// Describe turns a timeline entry into a sentence fragment that follows the
// actor's name, for example "moved todo → review".
func Describe(a model.Activity) string {
	switch a.Action {
	case "created":
		return "created the task"
	case "claimed":
		return "claimed the task (" + a.Detail + ")"
	case "lease_renewed":
		return "renewed the lease (" + a.Detail + ")"
	case "released":
		return "released the task"
	case "lease_expired":
		return "lease expired, " + a.Detail
	case "status":
		return "moved " + strings.ReplaceAll(a.Detail, "_", " ")
	case "updated":
		return "changed " + a.Detail
	case "done":
		return "completed the task"
	case "comment":
		return "commented"
	case "project_created":
		return "created project " + a.Detail
	}
	if a.Detail != "" {
		return a.Action + " " + a.Detail
	}
	return a.Action
}

// AvatarPaletteSize is the number of distinct avatar background colors
// (AvatarPalette's return value is always in [0, AvatarPaletteSize)). Kept
// small and fixed so colors stay visually distinct rather than shading into
// each other, and so agents sharing a kind reliably land on the same color.
const AvatarPaletteSize = 10

// AvatarPalette maps an agent's kind to a deterministic index into a small,
// fixed avatar color palette (see style.css's .avatar-0.. classes), so every
// agent reporting the same free-text kind ("claude-code", "agent",
// "builder", ...) is drawn with the same background color - the "similar
// ones reuse the same avatar" behavior - while an agent's own initials
// (Initials) keep individual agents visually distinguishable even when they
// share a color. kind has no fixed enum in this project (it is arbitrary,
// ad hoc free text set by whatever registered the agent), so this hashes
// the string with FNV-1a rather than looking it up in a maintained table:
// it handles any input, including "", without panicking, and never changes
// across calls (no time or randomness involved) or across process restarts.
func AvatarPalette(kind string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(kind)) // hash.Hash.Write on a Go hash never errors
	return int(h.Sum32() % AvatarPaletteSize)
}

// Initials returns up to two upper case letters for an avatar.
func Initials(name string) string {
	var out []rune
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || r == '.' || r == ' ' }) {
		for _, r := range part {
			out = append(out, r)
			break
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return strings.ToUpper(string(out))
}

// maxDepth bounds parent walks so a corrupt cycle cannot hang the UI.
const maxDepth = 8

func index(tasks []model.Task) map[string]model.Task {
	m := make(map[string]model.Task, len(tasks))
	for _, t := range tasks {
		m[t.ID] = t
	}
	return m
}

func epicOf(byID map[string]model.Task, t model.Task) string {
	for range maxDepth {
		if t.Type == model.KindEpic {
			return t.ID
		}
		p, ok := byID[t.Parent]
		if t.Parent == "" || !ok {
			return ""
		}
		t = p
	}
	return ""
}

// EpicOf returns the ID of the epic that t belongs to (t itself if it is an
// epic), or "" if it is not under one.
func EpicOf(tasks []model.Task, t model.Task) string { return epicOf(index(tasks), t) }

// InEpic returns the epic and everything beneath it. An empty epic returns
// tasks unchanged.
func InEpic(tasks []model.Task, epic string) []model.Task {
	if epic == "" {
		return tasks
	}
	byID := index(tasks)
	var out []model.Task
	for _, t := range tasks {
		if epicOf(byID, t) == epic {
			out = append(out, t)
		}
	}
	return out
}

// Epics returns the epics among tasks, in their given order.
func Epics(tasks []model.Task) []model.Task {
	var out []model.Task
	for _, t := range tasks {
		if t.Type == model.KindEpic {
			out = append(out, t)
		}
	}
	return out
}

// Children returns the direct children of the task with the given id.
func Children(tasks []model.Task, id string) []model.Task {
	var out []model.Task
	for _, t := range tasks {
		if t.Parent == id {
			out = append(out, t)
		}
	}
	return out
}

// Progress counts how many of ts are done.
func Progress(ts []model.Task) (done, total int) {
	for _, t := range ts {
		if t.Status == model.StatusDone {
			done++
		}
	}
	return done, len(ts)
}

// EpicProgress counts the done and total leaf tasks (kind task) beneath an
// epic, however deeply nested; stories are containers and do not count.
func EpicProgress(tasks []model.Task, epic string) (done, total int) {
	byID := index(tasks)
	for _, t := range tasks {
		if t.Type != model.KindTask || epicOf(byID, t) != epic {
			continue
		}
		total++
		if t.Status == model.StatusDone {
			done++
		}
	}
	return done, total
}

// ActorState says whether the named actor is a registered agent, whether it
// is running now (recent heartbeat) and which task it is working on.
type ActorState struct {
	Agent  bool   // a registered agent (people and "system" are not)
	Online bool   // heartbeat is recent: running now
	Task   string // the task it reports working on, if any
	Kind   string // the agent's free-text kind, "" if not a registered agent
}

// ActorOf looks the actor up among the registered agents.
func ActorOf(agents []model.Agent, name string) ActorState {
	for _, a := range agents {
		if a.Name == name {
			return ActorState{Agent: true, Online: a.Online, Task: a.CurrentTask, Kind: a.Kind}
		}
	}
	return ActorState{}
}

// ActorText renders an actor's state for humans: "working on AB-3",
// "online, idle", "finished" (a registered agent that is no longer sending
// heartbeats) or "" for people and the board itself.
func ActorText(s ActorState) string {
	switch {
	case !s.Agent:
		return ""
	case s.Online && s.Task != "":
		return "working on " + s.Task
	case s.Online:
		return "online, idle"
	}
	return "finished"
}

// SortAgentsByRecency returns a copy of agents ordered by most recent
// heartbeat first. Online agents, having heartbeated recently by
// definition, naturally sort to the top; an agent that has never sent a
// heartbeat (zero LastHeartbeat) sorts last.
func SortAgentsByRecency(agents []model.Agent) []model.Agent {
	out := append([]model.Agent(nil), agents...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastHeartbeat.After(out[j].LastHeartbeat) })
	return out
}

// Limit returns at most the first n elements of s (n <= 0 returns s
// unchanged), for panels that show a short list with a "view all" control.
func Limit[T any](s []T, n int) []T {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n]
}

// AgentSummary is a lifecycle count over an agent roster (AGENTBOARD-14).
// agentboard's agent list is append-only - there is no delete/prune - so
// Total is not "how many exist right now" in the sense a mutable roster
// would give, it is every name that has ever registered; Online/Offline
// split that same roster by current heartbeat state. There is no
// first-registered timestamp on Agent, so this reports counts only, never a
// fabricated "when" for any agent.
type AgentSummary struct {
	Total   int // every agent that has ever registered
	Online  int // recent heartbeat (Agent.Online)
	Offline int // Total - Online: lapsed, not deleted
}

// SummarizeAgents computes an AgentSummary over agents.
func SummarizeAgents(agents []model.Agent) AgentSummary {
	s := AgentSummary{Total: len(agents)}
	for _, a := range agents {
		if a.Online {
			s.Online++
		}
	}
	s.Offline = s.Total - s.Online
	return s
}

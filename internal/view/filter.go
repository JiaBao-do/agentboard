package view

import (
	"net/url"
	"sort"
	"strings"

	"github.com/JiaBao-do/agentboard/model"
)

// Unassigned is the sentinel value the assignee filter uses to mean "no
// agent is assigned". An empty Filter.Assignee means "assignee not
// filtered", so a distinct value is needed to ask for the unassigned set.
const Unassigned = "-unassigned-"

// Filter narrows which tasks the board shows. The zero value matches every
// task. Every non-empty field must match (a combinable filter bar, not a
// choice of alternatives); Query does a case-insensitive substring match
// against the task's ID, title, description and labels.
//
// Comment text is deliberately not searched. The board only keeps a capped,
// global slice of recent activity in memory (Snapshot.Activity), not each
// task's full history, so matching comments reliably would need either a
// new per-task fetch for every visible task or a server-side search index —
// more machinery than an in-memory client filter calls for. See
// docs/PITFALLS.md.
type Filter struct {
	Query    string
	Project  string
	Epic     string
	Status   model.Status
	Assignee string
	Type     model.Kind
	Priority model.Priority
}

// Empty reports whether f narrows nothing.
func (f Filter) Empty() bool { return f == Filter{} }

// Match reports whether t satisfies every set field of f.
func (f Filter) Match(t model.Task) bool {
	switch {
	case f.Project != "" && t.Project != f.Project:
		return false
	case f.Status != "" && t.Status != f.Status:
		return false
	case f.Type != "" && t.Type != f.Type:
		return false
	case f.Priority != "" && t.Priority != f.Priority:
		return false
	case f.Assignee == Unassigned && t.Assignee != "":
		return false
	case f.Assignee != "" && f.Assignee != Unassigned && t.Assignee != f.Assignee:
		return false
	}
	return f.Query == "" || matchQuery(t, f.Query)
}

// matchQuery does a case-insensitive substring match against fields cheap
// to search because they already live in the in-memory task list.
func matchQuery(t model.Task, q string) bool {
	q = strings.ToLower(q)
	if strings.Contains(strings.ToLower(t.ID), q) ||
		strings.Contains(strings.ToLower(t.Title), q) ||
		strings.Contains(strings.ToLower(t.Description), q) {
		return true
	}
	for _, l := range t.Labels {
		if strings.Contains(strings.ToLower(l), q) {
			return true
		}
	}
	return false
}

// FilterTasks returns the tasks of ts that match f, preserving order. An
// empty filter returns ts unchanged (no allocation).
func FilterTasks(ts []model.Task, f Filter) []model.Task {
	if f.Empty() {
		return ts
	}
	out := make([]model.Task, 0, len(ts))
	for _, t := range ts {
		if f.Match(t) {
			out = append(out, t)
		}
	}
	return out
}

// Assignees returns the distinct, non-empty assignees among tasks, sorted,
// for populating the assignee filter's options.
func Assignees(tasks []model.Task) []string {
	seen := make(map[string]bool)
	var out []string
	for _, t := range tasks {
		if t.Assignee != "" && !seen[t.Assignee] {
			seen[t.Assignee] = true
			out = append(out, t.Assignee)
		}
	}
	sort.Strings(out)
	return out
}

// FilterQuery encodes f as a URL query string (no leading "?"), omitting
// empty fields, so a filtered board view is shareable and survives a page
// reload through the address bar alone.
func FilterQuery(f Filter) string {
	v := url.Values{}
	set := func(k, s string) {
		if s != "" {
			v.Set(k, s)
		}
	}
	set("q", f.Query)
	set("project", f.Project)
	set("epic", f.Epic)
	set("status", string(f.Status))
	set("assignee", f.Assignee)
	set("type", string(f.Type))
	set("priority", string(f.Priority))
	return v.Encode()
}

// ParseFilterQuery decodes a URL query string (as from location.search,
// with or without its leading "?") into a Filter. Unknown keys are
// ignored, and an invalid status, type or priority value is dropped rather
// than rejected, so a stale or hand-edited link degrades to a less
// filtered view instead of an error.
func ParseFilterQuery(qs string) Filter {
	qs = strings.TrimPrefix(qs, "?")
	v, err := url.ParseQuery(qs)
	if err != nil {
		return Filter{}
	}
	f := Filter{Query: v.Get("q"), Project: v.Get("project"), Epic: v.Get("epic"), Assignee: v.Get("assignee")}
	if s := model.Status(v.Get("status")); s.Valid() {
		f.Status = s
	}
	if k := model.Kind(v.Get("type")); k.Valid() {
		f.Type = k
	}
	if p := model.Priority(v.Get("priority")); p.Valid() {
		f.Priority = p
	}
	return f
}

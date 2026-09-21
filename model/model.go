// Package model holds the plain data types shared by the agentboard server,
// client and WebAssembly UI. It has no dependencies beyond the standard
// library so that it stays small when compiled to WebAssembly.
package model

import (
	"slices"
	"time"
)

// Status is the workflow column a task is in.
type Status string

// Task statuses, in board column order.
const (
	StatusTodo       Status = "todo"
	StatusInProgress Status = "in_progress"
	StatusReview     Status = "review"
	StatusDone       Status = "done"
	StatusBlocked    Status = "blocked"
)

// Statuses lists every valid status in board column order.
var Statuses = []Status{StatusTodo, StatusInProgress, StatusReview, StatusDone, StatusBlocked}

// Valid reports whether s is a known status.
func (s Status) Valid() bool { return slices.Contains(Statuses, s) }

// Label returns a human readable column title.
func (s Status) Label() string {
	switch s {
	case StatusTodo:
		return "To do"
	case StatusInProgress:
		return "In progress"
	case StatusReview:
		return "Review"
	case StatusDone:
		return "Done"
	case StatusBlocked:
		return "Blocked"
	}
	return string(s)
}

// Priority orders tasks inside a column.
type Priority string

// Task priorities, lowest to highest.
const (
	PriorityLow    Priority = "low"
	PriorityMedium Priority = "medium"
	PriorityHigh   Priority = "high"
	PriorityUrgent Priority = "urgent"
)

// Priorities lists every valid priority, lowest first.
var Priorities = []Priority{PriorityLow, PriorityMedium, PriorityHigh, PriorityUrgent}

// Valid reports whether p is a known priority.
func (p Priority) Valid() bool { return slices.Contains(Priorities, p) }

// Rank returns 0 (low) to 3 (urgent); unknown priorities rank as low.
func (p Priority) Rank() int { return max(0, slices.Index(Priorities, p)) }

// Kind is the level of a task in the hierarchy: an epic contains stories, a
// story contains tasks (its subtasks).
type Kind string

// Task kinds, highest level first.
const (
	KindEpic  Kind = "epic"
	KindStory Kind = "story"
	KindTask  Kind = "task"
)

// Kinds lists every valid kind, highest level first.
var Kinds = []Kind{KindEpic, KindStory, KindTask}

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool { return slices.Contains(Kinds, k) }

// Level is 0 for epics, 1 for stories and 2 for tasks. A parent must have a
// lower level than its children, which also rules out cycles. An empty or
// unknown kind counts as a task.
func (k Kind) Level() int {
	if i := slices.Index(Kinds, k); i >= 0 {
		return i
	}
	return len(Kinds) - 1
}

// Project groups tasks under a short key such as "AB".
type Project struct {
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	NextSeq   int       `json:"next_seq"`
	CreatedAt time.Time `json:"created_at"`
}

// Task is a unit of work. While an agent works on it the task carries an
// Assignee and a lease; when the lease expires without a heartbeat the task
// returns to the todo column.
type Task struct {
	ID           string     `json:"id"`
	Project      string     `json:"project"`
	Type         Kind       `json:"type"`
	Parent       string     `json:"parent,omitempty"`
	Title        string     `json:"title"`
	Description  string     `json:"description,omitempty"`
	Status       Status     `json:"status"`
	Priority     Priority   `json:"priority"`
	Labels       []string   `json:"labels"`
	Assignee     string     `json:"assignee,omitempty"`
	LeaseExpires *time.Time `json:"lease_expires,omitempty"`
	LeaseSeconds int        `json:"lease_seconds,omitempty"`
	CreatedBy    string     `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// Agent is a worker (an AI agent, a script or a person) that reports in with
// heartbeats.
type Agent struct {
	Name          string    `json:"name"`
	Kind          string    `json:"kind"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
	CurrentTask   string    `json:"current_task,omitempty"`
	// Meta is free-form key/value data an agent reports about itself (model
	// name, host, version, ...). agentboard never interprets it.
	Meta map[string]string `json:"meta,omitempty"`
	// Online is derived from LastHeartbeat when a snapshot is built; it is
	// never trusted from storage.
	Online bool `json:"online"`
}

// Activity is one entry of the append-only timeline.
type Activity struct {
	ID     int64     `json:"id"`
	Time   time.Time `json:"time"`
	Actor  string    `json:"actor"`
	TaskID string    `json:"task_id,omitempty"`
	Action string    `json:"action"`
	Detail string    `json:"detail,omitempty"`
}

// SchemaVersion is the version of the persisted State format. It is
// written to every saved file so that newer data is never silently
// misread by older binaries; see docs/DATA_FORMAT.md.
const SchemaVersion = 1

// State is everything a Store persists.
type State struct {
	Version        int                 `json:"version"`
	Projects       map[string]*Project `json:"projects"`
	Tasks          map[string]*Task    `json:"tasks"`
	Agents         map[string]*Agent   `json:"agents"`
	Activity       []Activity          `json:"activity"`
	NextActivityID int64               `json:"next_activity_id"`
}

// NewState returns an empty, ready to use State.
func NewState() *State {
	return &State{
		Version:  SchemaVersion,
		Projects: map[string]*Project{},
		Tasks:    map[string]*Task{},
		Agents:   map[string]*Agent{},
	}
}

// Snapshot is the read model served to the UI: the whole board in one
// document plus the server clock, so clients need not trust their own.
type Snapshot struct {
	Now      time.Time  `json:"now"`
	Projects []Project  `json:"projects"`
	Tasks    []Task     `json:"tasks"`
	Agents   []Agent    `json:"agents"`
	Activity []Activity `json:"activity"` // newest first, capped
}

// TaskDetail is a task with its full timeline, oldest first.
type TaskDetail struct {
	Task     Task       `json:"task"`
	Activity []Activity `json:"activity"`
}

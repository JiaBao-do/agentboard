package agentboard

import "github.com/JiaBao-do/agentboard/model"

// Status is the workflow column a task is in. It re-exports model.Status so
// library users need only import this package.
type Status = model.Status

// Kind is a task's level in the epic, story, task hierarchy.
type Kind = model.Kind

// Priority orders tasks inside a column.
type Priority = model.Priority

// Project groups tasks under a short key such as "AB".
type Project = model.Project

// Task is a unit of work, possibly claimed by an agent under a lease.
type Task = model.Task

// Agent is a worker that reports in with heartbeats.
type Agent = model.Agent

// Activity is one entry of the append-only timeline.
type Activity = model.Activity

// State is everything a Store persists.
type State = model.State

// Snapshot is the whole board in one document, as served to the UI.
type Snapshot = model.Snapshot

// TaskDetail is a task with its full timeline.
type TaskDetail = model.TaskDetail

// SchemaVersion is the version of the persisted State format; see
// model.SchemaVersion.
const SchemaVersion = model.SchemaVersion

// Task statuses.
const (
	StatusTodo       = model.StatusTodo
	StatusInProgress = model.StatusInProgress
	StatusReview     = model.StatusReview
	StatusDone       = model.StatusDone
	StatusBlocked    = model.StatusBlocked
)

// Task kinds.
const (
	KindEpic  = model.KindEpic
	KindStory = model.KindStory
	KindTask  = model.KindTask
)

// Task priorities.
const (
	PriorityLow    = model.PriorityLow
	PriorityMedium = model.PriorityMedium
	PriorityHigh   = model.PriorityHigh
	PriorityUrgent = model.PriorityUrgent
)

// NewTask is the input for Board.AddTask and the body of POST /api/tasks.
type NewTask struct {
	Project     string   `json:"project"`
	Type        Kind     `json:"type,omitempty"`   // default task
	Parent      string   `json:"parent,omitempty"` // must sit at a higher level than Type
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Priority    Priority `json:"priority,omitempty"` // default medium
	Labels      []string `json:"labels,omitempty"`
	Actor       string   `json:"actor,omitempty"`
}

// Patch is a partial task update; nil fields are left unchanged. It is the
// body of PATCH /api/tasks/{id}.
type Patch struct {
	Title       *string   `json:"title,omitempty"`
	Description *string   `json:"description,omitempty"`
	Status      *Status   `json:"status,omitempty"`
	Priority    *Priority `json:"priority,omitempty"`
	Parent      *string   `json:"parent,omitempty"` // "" detaches from the current parent
	Labels      *[]string `json:"labels,omitempty"`
	// StartDate and EndDate set a task's Timeline dates (AGENTBOARD-6),
	// "YYYY-MM-DD" each. nil leaves the field unchanged, "" clears it (the
	// same tri-state convention Parent above already uses), and any other
	// value must parse as a date or the whole patch is rejected.
	StartDate *string `json:"start_date,omitempty"`
	EndDate   *string `json:"end_date,omitempty"`
	Actor     string  `json:"actor,omitempty"`
}

// Filter selects tasks for Board.Tasks. Empty fields match everything.
type Filter struct {
	Project  string
	Status   Status
	Assignee string
	Type     Kind
	Parent   string
}

// ProjectRequest is the body of POST /api/projects.
type ProjectRequest struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Actor string `json:"actor,omitempty"`
}

// ClaimRequest is the body of POST /api/tasks/{id}/claim.
type ClaimRequest struct {
	Agent        string `json:"agent"`
	LeaseSeconds int    `json:"lease_seconds,omitempty"` // default: board lease
}

// AgentRequest is the body of POST /api/tasks/{id}/release and /done.
type AgentRequest struct {
	Agent string `json:"agent"`
}

// CommentRequest is the body of POST /api/tasks/{id}/comment.
type CommentRequest struct {
	Actor string `json:"actor"`
	Text  string `json:"text"`
}

// HeartbeatRequest is the body of POST /api/agents/{name}/heartbeat.
type HeartbeatRequest struct {
	Kind string `json:"kind,omitempty"`
	Task string `json:"task,omitempty"` // task the agent is currently working on
	// Meta is optional free-form data about the agent; it replaces the
	// stored Meta when non-nil. At most 16 keys, keys up to 32 and values up
	// to 256 characters.
	Meta map[string]string `json:"meta,omitempty"`
}

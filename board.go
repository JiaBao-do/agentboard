// Package agentboard is a small Jira-style task board built for people who
// run AI agents: it shows every task's status and which agent is working on
// it. It ships as a library (Board, Store, Server, Client) and as a single
// binary whose web UI is Go compiled to WebAssembly and embedded in the
// package itself.
//
// # Leases
//
// An agent claims a task with a lease. As long as the agent keeps sending
// heartbeats the lease is extended; when heartbeats stop, the lease expires
// and the task automatically returns to the todo column so another agent can
// pick it up.
package agentboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JiaBao-do/agentboard/model"
)

// Sentinel errors, testable with errors.Is. The HTTP server maps them to
// status codes 404, 400, 409, 409 and 409.
var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid input")
	ErrClaimed  = errors.New("task is claimed by another agent")
	ErrNotOwner = errors.New("task is owned by another agent")
	ErrExists   = errors.New("already exists")
	// ErrBadCredentials is returned by Authenticate for both an unknown
	// email and a correct-email-wrong-password login: the same error and
	// (approximately) the same latency for both, so a caller cannot use
	// the response to discover which emails are registered. The HTTP
	// server maps it to 401.
	ErrBadCredentials = errors.New("invalid email or password")
)

const (
	defaultAgentTTL   = 2 * time.Minute
	defaultLease      = 10 * time.Minute
	minLease          = time.Second
	maxLease          = 24 * time.Hour
	snapshotActivity  = 100
	maxTitle          = 200
	maxDescription    = 20000
	maxComment        = 5000
	maxLabels         = 10
	maxKind           = 32
	maxMetaKeys       = 16
	maxMetaKey        = 32
	maxMetaValue      = 256
	systemActor       = "system"
	subscriberBufSize = 16
)

var (
	keyRE   = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}$`)
	nameRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	labelRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,31}$`)
)

// Event tells subscribers that something changed. It carries no payload
// beyond the hint: clients re-read the board.
type Event struct {
	Type   string    `json:"type"` // "task", "project", "agent" or "sweep"
	TaskID string    `json:"task_id,omitempty"`
	At     time.Time `json:"at"`
}

// Options configures Open. The zero value is a usable in-memory board.
type Options struct {
	// Store persists the board. Default: a fresh MemStore.
	Store Store
	// Now is the clock. Default: time.Now. Tests inject a fake.
	Now func() time.Time
	// AgentTTL is how long after its last heartbeat an agent counts as
	// online. Default: 2 minutes.
	AgentTTL time.Duration
	// Lease is the default task lease. Default: 10 minutes.
	Lease time.Duration
	// SaveMode selects when changes reach the Store: SaveAsync (default)
	// coalesces them in a background writer, SaveSync saves inline before
	// every call returns.
	SaveMode SaveMode
	// SaveDebounce is how long the async writer waits for more changes
	// before saving. Default: 200ms.
	SaveDebounce time.Duration
	// SaveMaxLatency caps how long continuous activity can postpone a save.
	// Default: 2s.
	SaveMaxLatency time.Duration
	// ArchiveDir, if set, receives old activity entries (see ReadArchive)
	// once the live log exceeds MaxActivity. Empty: never archive.
	ArchiveDir string
	// MaxActivity is the live activity log size that triggers archiving; the
	// newest half is kept. Default: 5000 entries.
	MaxActivity int
	// AllowedEmailDomains, if non-empty, restricts Register to email
	// addresses at these domains (case-insensitive, e.g. "example.com").
	// Empty (default): any syntactically valid email may register. This is
	// deliberately a runtime setting, never a hardcoded value, so the
	// public source carries no organization-specific domain; see
	// docs/PITFALLS.md.
	AllowedEmailDomains []string
}

// SaveMode selects the persistence strategy.
type SaveMode string

// Persistence strategies. Async can lose up to SaveMaxLatency of changes on a
// hard crash (kill -9, power loss); Flush, Close and a graceful shutdown never
// lose anything. Sync loses nothing but holds the board lock during disk I/O.
const (
	SaveAsync SaveMode = "async"
	SaveSync  SaveMode = "sync"
)

// Board is the task board. All methods are safe for concurrent use.
type Board struct {
	mu                  sync.Mutex
	st                  *model.State
	store               Store
	now                 func() time.Time
	agentTTL            time.Duration
	lease               time.Duration
	subs                map[int]chan Event
	nextSub             int
	allowedEmailDomains []string // lower case, normalized once in Open

	// Write-behind persistence. saveMu serialises saves (lock order: saveMu,
	// then mu). The fields below it are guarded by mu.
	saveMu      sync.Mutex
	archiveDir  string
	maxActivity int
	mode        SaveMode
	debounce    time.Duration
	maxLatency  time.Duration
	kick        chan struct{} // capacity 1: a non-blocking send coalesces bursts
	stop        chan struct{}
	wg          sync.WaitGroup
	closeOnce   sync.Once
	closed      bool
	dirty       bool
	dirtySince  time.Time
	lastSave    time.Time
	lastErr     error
	saves       int64
	failures    int64
}

// Open loads the state from the configured store and returns a Board.
func Open(o Options) (*Board, error) {
	if o.Store == nil {
		o.Store = NewMemStore()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.AgentTTL <= 0 {
		o.AgentTTL = defaultAgentTTL
	}
	if o.Lease <= 0 {
		o.Lease = defaultLease
	}
	if o.SaveMode == "" {
		o.SaveMode = SaveAsync
	}
	if o.SaveMode != SaveAsync && o.SaveMode != SaveSync {
		return nil, invalid("save mode %q must be async or sync", o.SaveMode)
	}
	if o.SaveDebounce <= 0 {
		o.SaveDebounce = 200 * time.Millisecond
	}
	if o.SaveMaxLatency <= 0 {
		o.SaveMaxLatency = 2 * time.Second
	}
	if o.MaxActivity <= 0 {
		o.MaxActivity = 5000
	}
	st, err := o.Store.Load()
	if err != nil {
		return nil, err
	}
	var domains []string
	for _, d := range o.AllowedEmailDomains {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			domains = append(domains, d)
		}
	}
	b := &Board{
		st: st, store: o.Store, now: o.Now,
		agentTTL: o.AgentTTL, lease: o.Lease,
		subs:       map[int]chan Event{},
		archiveDir: o.ArchiveDir, maxActivity: o.MaxActivity,
		mode: o.SaveMode, debounce: o.SaveDebounce, maxLatency: o.SaveMaxLatency,
		kick: make(chan struct{}, 1), stop: make(chan struct{}),
		allowedEmailDomains: domains,
	}
	if b.mode == SaveAsync {
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			b.writer()
		}()
	}
	return b, nil
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// do runs fn under the board lock after sweeping expired leases, then saves
// and notifies subscribers if anything changed. fn must validate before it
// mutates: returning an error means "nothing changed".
func (b *Board) do(fn func(now time.Time) (*Event, error)) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now().UTC()
	swept := b.sweepLocked(now) > 0
	ev, err := fn(now)
	if ev == nil && swept {
		ev = &Event{Type: "sweep"} // expired leases changed state even if fn did not
	}
	if ev == nil {
		return err
	}
	if serr := b.persistAndPublishLocked(now, ev); err == nil {
		err = serr
	}
	return err
}

// persistAndPublishLocked records that state changed, saves it inline in sync
// mode (or after Close) or wakes the writer, and tells subscribers.
func (b *Board) persistAndPublishLocked(now time.Time, ev *Event) error {
	var err error
	if !b.dirty {
		b.dirty, b.dirtySince = true, now
	}
	if b.mode == SaveSync || b.closed {
		if err = b.store.Save(b.st); err != nil {
			b.lastErr = err
			b.failures++
		} else {
			b.dirty, b.lastErr, b.lastSave = false, nil, now
			b.saves++
		}
	} else {
		select {
		case b.kick <- struct{}{}:
		default: // a wake-up is already pending
		}
	}
	b.publishLocked(now, ev)
	return err
}

func (b *Board) publishLocked(now time.Time, ev *Event) {
	ev.At = now
	for _, ch := range b.subs {
		select {
		case ch <- *ev:
		default: // slow subscriber: it will catch up on the next event
		}
	}
}

// writer is the single background goroutine that persists changes. It
// sleeps until kicked, waits for the burst to settle (debounce, but never
// longer than maxLatency since the first change), then saves one snapshot.
// A failed save is retried with exponential backoff until it succeeds or the
// board is closed; the dirty flag stays set meanwhile.
func (b *Board) writer() {
	for {
		select {
		case <-b.kick:
		case <-b.stop:
			return
		}
		first := time.Now()
		timer := time.NewTimer(b.debounce)
	settle:
		for {
			select {
			case <-b.kick:
				left := b.maxLatency - time.Since(first)
				if left <= 0 {
					break settle
				}
				timer.Reset(min(b.debounce, left))
			case <-timer.C:
				break settle
			case <-b.stop:
				timer.Stop()
				return
			}
		}
		timer.Stop()
		backoff := 100 * time.Millisecond
		for b.saveNow() != nil {
			select {
			case <-time.After(backoff):
				backoff = min(backoff*2, 5*time.Second)
			case <-b.stop:
				return
			}
		}
	}
}

// saveNow saves a consistent snapshot if there are unsaved changes. The
// snapshot is taken under the board lock; the disk I/O happens after it is
// released, so mutations are never blocked by a slow disk.
func (b *Board) saveNow() error {
	b.saveMu.Lock()
	defer b.saveMu.Unlock()
	if err := b.archiveOld(); err != nil {
		b.mu.Lock()
		b.lastErr = err
		b.failures++
		b.mu.Unlock()
		return err
	}
	b.mu.Lock()
	if !b.dirty {
		b.mu.Unlock()
		return nil
	}
	snap := b.snapshotStateLocked()
	b.dirty = false
	b.mu.Unlock()

	err := b.store.Save(snap)

	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now().UTC()
	if err != nil {
		b.dirty = true // keep the changes pending; the writer will retry
		b.lastErr = err
		b.failures++
	} else {
		b.lastErr, b.lastSave = nil, now
		b.saves++
		if b.dirty { // more changes arrived while saving
			select {
			case b.kick <- struct{}{}:
			default:
			}
		}
	}
	b.publishLocked(now, &Event{Type: "save"})
	return err
}

// archiveOld moves the oldest activity entries to the archive files when the
// live log is too long. The archive is written first, then the entries are
// dropped from the state (which marks it dirty so the shorter log is
// saved): a crash in between duplicates entries in the archive, never loses
// them. Called with saveMu held, so it never runs concurrently with a save.
func (b *Board) archiveOld() error {
	b.mu.Lock()
	if b.archiveDir == "" || len(b.st.Activity) <= b.maxActivity {
		b.mu.Unlock()
		return nil
	}
	cut := len(b.st.Activity) - b.maxActivity/2
	batch := append([]model.Activity(nil), b.st.Activity[:cut]...)
	cutID := batch[len(batch)-1].ID
	b.mu.Unlock()

	if err := appendArchive(b.archiveDir, batch); err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	i := 0
	for i < len(b.st.Activity) && b.st.Activity[i].ID <= cutID {
		i++
	}
	b.st.Activity = append([]model.Activity(nil), b.st.Activity[i:]...) // fresh array: snapshots never alias it
	if cutID > b.st.ArchivedThrough {
		b.st.ArchivedThrough = cutID
	}
	if !b.dirty {
		b.dirty, b.dirtySince = true, b.now().UTC()
	}
	return nil
}

// snapshotStateLocked deep-copies the state for saving outside the lock. The
// activity log is append-only, so the copy shares its backing array but
// caps the slice so later appends cannot alias it.
func (b *Board) snapshotStateLocked() *model.State {
	c := &model.State{
		Version:         b.st.Version,
		Projects:        make(map[string]*model.Project, len(b.st.Projects)),
		Tasks:           make(map[string]*model.Task, len(b.st.Tasks)),
		Agents:          make(map[string]*model.Agent, len(b.st.Agents)),
		Users:           make(map[string]*model.User, len(b.st.Users)),
		Activity:        b.st.Activity[:len(b.st.Activity):len(b.st.Activity)],
		NextActivityID:  b.st.NextActivityID,
		ArchivedThrough: b.st.ArchivedThrough,
	}
	for k, p := range b.st.Projects {
		cp := *p
		c.Projects[k] = &cp
	}
	for k, t := range b.st.Tasks {
		ct := cloneTask(t)
		c.Tasks[k] = &ct
	}
	for k, a := range b.st.Agents {
		ca := cloneAgent(a)
		c.Agents[k] = &ca
	}
	for k, u := range b.st.Users {
		cu := cloneUser(u)
		c.Users[k] = &cu
	}
	return c
}

// Flush blocks until every change made before the call is in the Store, and
// returns the save error, if any. Use it before reading the Store directly
// (export, import, backups).
func (b *Board) Flush(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return b.saveNow()
}

// Close stops the background writer and performs a final synchronous save.
// It is idempotent and safe to call from several goroutines. Changes made
// after Close are saved inline.
func (b *Board) Close(ctx context.Context) error {
	b.closeOnce.Do(func() {
		close(b.stop)
		b.wg.Wait()
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()
	})
	return b.Flush(ctx)
}

// SaveStatus reports persistence health: whether changes are pending, when
// the last save happened and the last error.
func (b *Board) SaveStatus() model.SaveStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.saveStatusLocked(b.now().UTC())
}

func (b *Board) saveStatusLocked(now time.Time) model.SaveStatus {
	s := model.SaveStatus{
		Mode: string(b.mode), Dirty: b.dirty, LastSave: b.lastSave,
		Saves: b.saves, Failures: b.failures,
	}
	if b.lastErr != nil {
		s.LastError = b.lastErr.Error()
	}
	if b.dirty {
		s.LagMillis = now.Sub(b.dirtySince).Milliseconds()
	}
	return s
}

// Subscribe returns a channel of change events and a cancel function. The
// channel is buffered; if a subscriber falls behind, events are dropped
// rather than blocking the board.
func (b *Board) Subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextSub
	b.nextSub++
	ch := make(chan Event, subscriberBufSize)
	b.subs[id] = ch
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(ch)
		}
	}
}

func (b *Board) log(now time.Time, actor, taskID, action, detail string) {
	b.st.NextActivityID++
	b.st.Activity = append(b.st.Activity, model.Activity{
		ID: b.st.NextActivityID, Time: now, Actor: actor,
		TaskID: taskID, Action: action, Detail: detail,
	})
}

// sweepLocked returns in-progress tasks whose lease expired to the todo
// column and reports how many it released.
func (b *Board) sweepLocked(now time.Time) int {
	var expired []string
	for id, t := range b.st.Tasks {
		if t.Status == model.StatusInProgress && t.LeaseExpires != nil && !now.Before(*t.LeaseExpires) {
			expired = append(expired, id)
		}
	}
	sort.Slice(expired, func(i, j int) bool { return idLess(expired[i], expired[j]) })
	for _, id := range expired {
		t := b.st.Tasks[id]
		prev := t.Assignee
		t.Status, t.Assignee, t.LeaseExpires, t.LeaseSeconds = model.StatusTodo, "", nil, 0
		t.UpdatedAt, t.UpdatedBy = now, systemActor
		b.clearCurrentLocked(prev, id)
		b.log(now, systemActor, id, "lease_expired", "released from "+prev)
	}
	return len(expired)
}

// Sweep releases tasks whose lease has expired and returns how many it
// released. The server calls it periodically; every other Board method also
// sweeps first, so calling it is optional.
func (b *Board) Sweep() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now().UTC()
	n := b.sweepLocked(now)
	if n > 0 {
		_ = b.persistAndPublishLocked(now, &Event{Type: "sweep"})
	}
	return n
}

func (b *Board) clearCurrentLocked(agent, taskID string) {
	if a := b.st.Agents[agent]; a != nil && a.CurrentTask == taskID {
		a.CurrentTask = ""
	}
}

func (b *Board) touchAgentLocked(name, kind, taskID string, now time.Time) *model.Agent {
	a := b.st.Agents[name]
	if a == nil {
		a = &model.Agent{Name: name, Kind: "agent"}
		b.st.Agents[name] = a
	}
	if kind != "" {
		a.Kind = kind
	}
	a.LastHeartbeat = now
	if taskID != "" {
		a.CurrentTask = taskID
	}
	return a
}

func (b *Board) taskLocked(id string) (*model.Task, error) {
	t := b.st.Tasks[id]
	if t == nil {
		return nil, fmt.Errorf("%w: task %q", ErrNotFound, id)
	}
	return t, nil
}

// requireActor validates who is making a change. Writes are never
// anonymous: the actor is the registered agent name (or a person's display
// name such as "user"). The name "system" is reserved for the board itself.
func requireActor(actor string) (string, error) {
	switch {
	case actor == "":
		return "", invalid("actor required: say who you are (agent name, X-Agent-Name header, -agent or AGENTBOARD_AGENT)")
	case actor == systemActor:
		return "", invalid("actor %q is reserved for the board itself", actor)
	case !nameRE.MatchString(actor):
		return "", invalid("actor %q must match %s", actor, nameRE)
	}
	return actor, nil
}

func normalizeLabels(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, l := range in {
		l = strings.ToLower(strings.TrimSpace(l))
		if l == "" {
			continue
		}
		if !labelRE.MatchString(l) {
			return nil, invalid("label %q must match %s", l, labelRE)
		}
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	if len(out) > maxLabels {
		return nil, invalid("at most %d labels", maxLabels)
	}
	return out, nil
}

func validateMeta(m map[string]string) error {
	if len(m) > maxMetaKeys {
		return invalid("meta has more than %d keys", maxMetaKeys)
	}
	for k, v := range m {
		if k == "" || len(k) > maxMetaKey || len(v) > maxMetaValue {
			return invalid("meta keys must be 1-%d and values at most %d characters", maxMetaKey, maxMetaValue)
		}
	}
	return nil
}

func cloneAgent(a *model.Agent) model.Agent {
	c := *a
	c.Meta = maps.Clone(a.Meta)
	return c
}

func cloneUser(u *model.User) model.User {
	c := *u
	c.PasswordHash = append([]byte(nil), u.PasswordHash...)
	c.Salt = append([]byte(nil), u.Salt...)
	return c
}

func cloneTask(t *model.Task) model.Task {
	c := *t
	c.Labels = append([]string{}, t.Labels...)
	if t.LeaseExpires != nil {
		e := *t.LeaseExpires
		c.LeaseExpires = &e
	}
	if t.StartDate != nil {
		d := *t.StartDate
		c.StartDate = &d
	}
	if t.EndDate != nil {
		d := *t.EndDate
		c.EndDate = &d
	}
	return c
}

// datePtrEqual reports whether a and b name the same date, treating two nil
// pointers (both "unset") as equal.
func datePtrEqual(a, b *model.Date) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return a.Equal(b.Time)
	}
}

// idLess orders task IDs like "AB-2" before "AB-10".
func idLess(a, b string) bool {
	pa, na := splitID(a)
	pb, nb := splitID(b)
	if pa != pb {
		return pa < pb
	}
	return na < nb
}

func splitID(id string) (string, int) {
	p, n, _ := strings.Cut(id, "-")
	v, _ := strconv.Atoi(n)
	return p, v
}

// checkParentLocked verifies that parent (if any) exists in the same project
// and sits at a strictly higher level than a task of the given kind.
func (b *Board) checkParentLocked(project string, kind model.Kind, parent string) error {
	if parent == "" {
		return nil
	}
	pt := b.st.Tasks[parent]
	switch {
	case pt == nil:
		return fmt.Errorf("%w: parent %q", ErrNotFound, parent)
	case pt.Project != project:
		return invalid("parent %s is in project %s, not %s", parent, pt.Project, project)
	case pt.Type.Level() >= kind.Level():
		return invalid("a %s cannot have a %s as its parent", kind, pt.Type)
	}
	return nil
}

// CreateProject adds a project. The key is 2-10 upper case letters or digits
// starting with a letter; task IDs are "<key>-<n>".
func (b *Board) CreateProject(key, name, actor string) (Project, error) {
	name = strings.TrimSpace(name)
	if !keyRE.MatchString(key) {
		return Project{}, invalid("project key %q must match %s", key, keyRE)
	}
	if name == "" || len(name) > maxTitle {
		return Project{}, invalid("project name must be 1-%d characters", maxTitle)
	}
	actor, err := requireActor(actor)
	if err != nil {
		return Project{}, err
	}
	var out Project
	err = b.do(func(now time.Time) (*Event, error) {
		if b.st.Projects[key] != nil {
			return nil, fmt.Errorf("%w: project %s", ErrExists, key)
		}
		p := &model.Project{Key: key, Name: name, CreatedAt: now}
		b.st.Projects[key] = p
		b.log(now, actor, "", "project_created", key+" "+name)
		out = *p
		return &Event{Type: "project"}, nil
	})
	return out, err
}

// AddTask creates a task in the todo column.
func (b *Board) AddTask(in NewTask) (Task, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" || len(title) > maxTitle {
		return Task{}, invalid("title must be 1-%d characters", maxTitle)
	}
	if len(in.Description) > maxDescription {
		return Task{}, invalid("description must be at most %d characters", maxDescription)
	}
	prio := in.Priority
	if prio == "" {
		prio = model.PriorityMedium
	}
	if !prio.Valid() {
		return Task{}, invalid("priority %q must be one of low, medium, high, urgent", prio)
	}
	kind := in.Type
	if kind == "" {
		kind = model.KindTask
	}
	if !kind.Valid() {
		return Task{}, invalid("type %q must be one of epic, story, task", kind)
	}
	labels, err := normalizeLabels(in.Labels)
	if err != nil {
		return Task{}, err
	}
	actor, err := requireActor(in.Actor)
	if err != nil {
		return Task{}, err
	}
	var out Task
	err = b.do(func(now time.Time) (*Event, error) {
		p := b.st.Projects[in.Project]
		if p == nil {
			return nil, fmt.Errorf("%w: project %q", ErrNotFound, in.Project)
		}
		if err := b.checkParentLocked(p.Key, kind, in.Parent); err != nil {
			return nil, err
		}
		p.NextSeq++
		t := &model.Task{
			ID: fmt.Sprintf("%s-%d", p.Key, p.NextSeq), Project: p.Key,
			Type: kind, Parent: in.Parent,
			Title: title, Description: in.Description,
			Status: model.StatusTodo, Priority: prio, Labels: labels,
			CreatedBy: actor, UpdatedBy: actor, CreatedAt: now, UpdatedAt: now,
		}
		b.st.Tasks[t.ID] = t
		b.log(now, actor, t.ID, "created", title)
		out = cloneTask(t)
		return &Event{Type: "task", TaskID: t.ID}, nil
	})
	return out, err
}

// Claim assigns the task to agent with a lease and moves it to in_progress.
// It fails with ErrClaimed while another agent holds an unexpired lease. An
// agent may claim again to renew its own lease. A zero lease uses the board
// default.
func (b *Board) Claim(id, agent string, lease time.Duration) (Task, error) {
	if !nameRE.MatchString(agent) {
		return Task{}, invalid("agent %q must match %s", agent, nameRE)
	}
	if lease == 0 {
		lease = b.lease
	}
	if lease < minLease || lease > maxLease {
		return Task{}, invalid("lease must be between %s and %s", minLease, maxLease)
	}
	var out Task
	err := b.do(func(now time.Time) (*Event, error) {
		t, err := b.taskLocked(id)
		if err != nil {
			return nil, err
		}
		if t.Status == model.StatusDone {
			return nil, invalid("task %s is done", id)
		}
		if t.Assignee != "" && t.Assignee != agent && t.LeaseExpires != nil {
			return nil, fmt.Errorf("%w: %s holds %s until %s", ErrClaimed, t.Assignee, id, t.LeaseExpires.Format(time.RFC3339))
		}
		renewal := t.Assignee == agent && t.Status == model.StatusInProgress
		exp := now.Add(lease)
		t.Assignee, t.Status = agent, model.StatusInProgress
		t.LeaseExpires, t.LeaseSeconds, t.UpdatedAt, t.UpdatedBy = &exp, int(lease/time.Second), now, agent
		b.touchAgentLocked(agent, "", id, now)
		action := "claimed"
		if renewal {
			action = "lease_renewed"
		}
		b.log(now, agent, id, action, "lease "+lease.String())
		out = cloneTask(t)
		return &Event{Type: "task", TaskID: id}, nil
	})
	return out, err
}

// Release gives a claimed task back to the todo column. Only the assignee
// may release it.
func (b *Board) Release(id, agent string) (Task, error) {
	var out Task
	err := b.do(func(now time.Time) (*Event, error) {
		t, err := b.taskLocked(id)
		if err != nil {
			return nil, err
		}
		if t.Assignee == "" {
			return nil, invalid("task %s is not claimed", id)
		}
		if t.Assignee != agent {
			return nil, fmt.Errorf("%w: %s belongs to %s", ErrNotOwner, id, t.Assignee)
		}
		t.Status, t.Assignee, t.LeaseExpires, t.LeaseSeconds, t.UpdatedAt, t.UpdatedBy = model.StatusTodo, "", nil, 0, now, agent
		b.clearCurrentLocked(agent, id)
		b.log(now, agent, id, "released", "")
		out = cloneTask(t)
		return &Event{Type: "task", TaskID: id}, nil
	})
	return out, err
}

// Complete marks the task done. If another agent holds an unexpired lease
// the call fails with ErrNotOwner.
func (b *Board) Complete(id, agent string) (Task, error) {
	if !nameRE.MatchString(agent) {
		return Task{}, invalid("agent %q must match %s", agent, nameRE)
	}
	var out Task
	err := b.do(func(now time.Time) (*Event, error) {
		t, err := b.taskLocked(id)
		if err != nil {
			return nil, err
		}
		if t.Assignee != "" && t.Assignee != agent && t.LeaseExpires != nil {
			return nil, fmt.Errorf("%w: %s belongs to %s", ErrNotOwner, id, t.Assignee)
		}
		if t.Status == model.StatusDone {
			return nil, invalid("task %s is already done", id)
		}
		if t.Assignee == "" {
			t.Assignee = agent
		}
		t.Status, t.LeaseExpires, t.LeaseSeconds, t.UpdatedAt, t.UpdatedBy = model.StatusDone, nil, 0, now, agent
		b.clearCurrentLocked(t.Assignee, id)
		b.log(now, agent, id, "done", "")
		out = cloneTask(t)
		return &Event{Type: "task", TaskID: id}, nil
	})
	return out, err
}

// Update applies a partial update. Moving a task changes its lease: entering
// todo clears the assignee, entering review, blocked or done drops the lease
// but keeps the assignee so the board still shows who did the work.
func (b *Board) Update(id string, p Patch) (Task, error) {
	actor, err := requireActor(p.Actor)
	if err != nil {
		return Task{}, err
	}
	if p.Title != nil {
		if v := strings.TrimSpace(*p.Title); v == "" || len(v) > maxTitle {
			return Task{}, invalid("title must be 1-%d characters", maxTitle)
		}
	}
	if p.Description != nil && len(*p.Description) > maxDescription {
		return Task{}, invalid("description must be at most %d characters", maxDescription)
	}
	if p.Status != nil && !p.Status.Valid() {
		return Task{}, invalid("status %q must be one of todo, in_progress, review, done, blocked", *p.Status)
	}
	if p.Priority != nil && !p.Priority.Valid() {
		return Task{}, invalid("priority %q must be one of low, medium, high, urgent", *p.Priority)
	}
	var labels []string
	if p.Labels != nil {
		if labels, err = normalizeLabels(*p.Labels); err != nil {
			return Task{}, err
		}
	}
	// StartDate/EndDate use the same tri-state convention as Parent: nil
	// leaves the field alone, an empty string clears it, anything else must
	// parse as a date. Parsed here (outside the lock) so a malformed date
	// never reaches b.do; the combined start/end ordering is checked once
	// the closure knows the resulting values (below), before either field is
	// mutated on the live task.
	var startDate, endDate *model.Date
	if p.StartDate != nil && *p.StartDate != "" {
		d, derr := model.ParseDate(*p.StartDate)
		if derr != nil {
			return Task{}, invalid("start date: %v", derr)
		}
		startDate = &d
	}
	if p.EndDate != nil && *p.EndDate != "" {
		d, derr := model.ParseDate(*p.EndDate)
		if derr != nil {
			return Task{}, invalid("end date: %v", derr)
		}
		endDate = &d
	}
	var out Task
	err = b.do(func(now time.Time) (*Event, error) {
		t, err := b.taskLocked(id)
		if err != nil {
			return nil, err
		}
		if p.Parent != nil && *p.Parent != t.Parent {
			if err := b.checkParentLocked(t.Project, t.Type, *p.Parent); err != nil {
				return nil, err
			}
		}
		finalStart, finalEnd := t.StartDate, t.EndDate
		if p.StartDate != nil {
			finalStart = startDate
		}
		if p.EndDate != nil {
			finalEnd = endDate
		}
		if finalStart != nil && finalEnd != nil && finalEnd.Before(*finalStart) {
			return nil, invalid("end date %s is before start date %s", finalEnd, finalStart)
		}
		var changed []string
		if p.Title != nil && strings.TrimSpace(*p.Title) != t.Title {
			t.Title = strings.TrimSpace(*p.Title)
			changed = append(changed, "title")
		}
		if p.Description != nil && *p.Description != t.Description {
			t.Description = *p.Description
			changed = append(changed, "description")
		}
		if p.Priority != nil && *p.Priority != t.Priority {
			t.Priority = *p.Priority
			changed = append(changed, "priority")
		}
		if p.Labels != nil {
			t.Labels = labels
			changed = append(changed, "labels")
		}
		if p.Parent != nil && *p.Parent != t.Parent {
			t.Parent = *p.Parent
			changed = append(changed, "parent")
		}
		if p.StartDate != nil && !datePtrEqual(t.StartDate, finalStart) {
			t.StartDate = finalStart
			changed = append(changed, "start_date")
		}
		if p.EndDate != nil && !datePtrEqual(t.EndDate, finalEnd) {
			t.EndDate = finalEnd
			changed = append(changed, "end_date")
		}
		if len(changed) > 0 {
			b.log(now, actor, id, "updated", strings.Join(changed, ", "))
		}
		if p.Status != nil && *p.Status != t.Status {
			from := t.Status
			b.moveLocked(t, *p.Status)
			b.log(now, actor, id, "status", fmt.Sprintf("%s → %s", from, t.Status))
			changed = append(changed, "status")
		}
		if len(changed) == 0 {
			out = cloneTask(t)
			return nil, nil
		}
		t.UpdatedAt, t.UpdatedBy = now, actor
		out = cloneTask(t)
		return &Event{Type: "task", TaskID: id}, nil
	})
	return out, err
}

func (b *Board) moveLocked(t *model.Task, to model.Status) {
	if t.Status == model.StatusInProgress && to != model.StatusInProgress {
		b.clearCurrentLocked(t.Assignee, t.ID)
	}
	t.Status = to
	t.LeaseExpires, t.LeaseSeconds = nil, 0
	if to == model.StatusTodo {
		t.Assignee = ""
	}
}

// Comment appends a comment to the task's timeline.
func (b *Board) Comment(id, actor, text string) error {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > maxComment {
		return invalid("comment must be 1-%d characters", maxComment)
	}
	actor, err := requireActor(actor)
	if err != nil {
		return err
	}
	return b.do(func(now time.Time) (*Event, error) {
		t, err := b.taskLocked(id)
		if err != nil {
			return nil, err
		}
		t.UpdatedAt, t.UpdatedBy = now, actor
		b.log(now, actor, id, "comment", text)
		return &Event{Type: "task", TaskID: id}, nil
	})
}

// Heartbeat records that the named agent is alive, creating it on first use,
// and extends the lease of every in-progress task the agent holds. If taskID
// is set it must be assigned to the agent; it becomes the agent's current
// task.
func (b *Board) Heartbeat(name string, req HeartbeatRequest) (Agent, error) {
	kind, taskID := req.Kind, req.Task
	if !nameRE.MatchString(name) {
		return Agent{}, invalid("agent %q must match %s", name, nameRE)
	}
	if len(kind) > maxKind {
		return Agent{}, invalid("kind must be at most %d characters", maxKind)
	}
	if err := validateMeta(req.Meta); err != nil {
		return Agent{}, err
	}
	var out Agent
	err := b.do(func(now time.Time) (*Event, error) {
		if taskID != "" {
			t, err := b.taskLocked(taskID)
			if err != nil {
				return nil, err
			}
			if t.Assignee != name {
				return nil, fmt.Errorf("%w: %s is not assigned to %s", ErrNotOwner, taskID, name)
			}
		}
		a := b.touchAgentLocked(name, kind, taskID, now)
		if req.Meta != nil {
			a.Meta = maps.Clone(req.Meta)
		}
		for _, t := range b.st.Tasks {
			if t.Assignee == name && t.Status == model.StatusInProgress && t.LeaseExpires != nil {
				d := time.Duration(t.LeaseSeconds) * time.Second
				if d <= 0 {
					d = b.lease
				}
				exp := now.Add(d)
				t.LeaseExpires = &exp
			}
		}
		out = cloneAgent(a)
		out.Online = true
		return &Event{Type: "agent"}, nil
	})
	return out, err
}

// Tasks lists tasks matching f, ordered by project and creation order.
func (b *Board) Tasks(f Filter) []Task {
	var out []Task
	_ = b.do(func(time.Time) (*Event, error) {
		out = b.tasksLocked(f)
		return nil, nil
	})
	return out
}

func (b *Board) tasksLocked(f Filter) []Task {
	out := make([]Task, 0, len(b.st.Tasks))
	for _, t := range b.st.Tasks {
		if f.Project != "" && t.Project != f.Project {
			continue
		}
		if f.Status != "" && t.Status != f.Status {
			continue
		}
		if f.Assignee != "" && t.Assignee != f.Assignee {
			continue
		}
		if f.Type != "" && t.Type != f.Type {
			continue
		}
		if f.Parent != "" && t.Parent != f.Parent {
			continue
		}
		out = append(out, cloneTask(t))
	}
	sort.Slice(out, func(i, j int) bool { return idLess(out[i].ID, out[j].ID) })
	return out
}

// Task returns one task with its full timeline, oldest entry first.
func (b *Board) Task(id string) (TaskDetail, error) {
	var out TaskDetail
	err := b.do(func(time.Time) (*Event, error) {
		t, err := b.taskLocked(id)
		if err != nil {
			return nil, err
		}
		out.Task = cloneTask(t)
		out.Activity = []Activity{}
		for _, a := range b.st.Activity {
			if a.TaskID == id {
				out.Activity = append(out.Activity, a)
			}
		}
		return nil, nil
	})
	return out, err
}

// Agents lists known agents, ordered by name, with Online derived from the
// heartbeat and the board's AgentTTL.
func (b *Board) Agents() []Agent {
	var out []Agent
	_ = b.do(func(now time.Time) (*Event, error) {
		out = b.agentsLocked(now)
		return nil, nil
	})
	return out
}

func (b *Board) agentsLocked(now time.Time) []Agent {
	out := make([]Agent, 0, len(b.st.Agents))
	for _, a := range b.st.Agents {
		c := cloneAgent(a)
		c.Online = now.Sub(a.LastHeartbeat) < b.agentTTL
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Projects lists projects ordered by key.
func (b *Board) Projects() []Project {
	var out []Project
	_ = b.do(func(time.Time) (*Event, error) {
		out = b.projectsLocked()
		return nil, nil
	})
	return out
}

func (b *Board) projectsLocked() []Project {
	out := make([]Project, 0, len(b.st.Projects))
	for _, p := range b.st.Projects {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Snapshot returns the whole board in one consistent read.
func (b *Board) Snapshot() Snapshot {
	var s Snapshot
	_ = b.do(func(now time.Time) (*Event, error) {
		s = Snapshot{
			Now:      now,
			Projects: b.projectsLocked(),
			Tasks:    b.tasksLocked(Filter{}),
			Agents:   b.agentsLocked(now),
			Activity: []Activity{},
			Save:     b.saveStatusLocked(now),
		}
		for i := len(b.st.Activity) - 1; i >= 0 && len(s.Activity) < snapshotActivity; i-- {
			s.Activity = append(s.Activity, b.st.Activity[i])
		}
		return nil, nil
	})
	return s
}

// Recent returns the newest activity entries, newest first.
func (b *Board) Recent(limit int) []Activity {
	if limit <= 0 || limit > 1000 {
		limit = snapshotActivity
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []Activity{}
	for i := len(b.st.Activity) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, b.st.Activity[i])
	}
	return out
}

// Export returns a deep copy of the whole persisted state, suitable for
// EncodeState. It is the basis of backups and of moving a board between
// machines.
func (b *Board) Export() (*State, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sweepLocked(b.now().UTC())
	raw, err := json.Marshal(b.st)
	if err != nil {
		return nil, err
	}
	return DecodeState(raw)
}

// Shutdown records a final "server_stopped" activity entry and marks every
// agent offline (its last heartbeat is moved back by the agent TTL) so the
// board does not claim agents are alive while nobody is serving it. Leases
// are left alone: agents that resume heartbeating after a restart keep their
// tasks. The final state is saved before Shutdown returns.
func (b *Board) Shutdown(actor string) error {
	if actor == "" {
		actor = systemActor
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now().UTC()
	b.sweepLocked(now)
	for _, a := range b.st.Agents {
		if stale := now.Add(-b.agentTTL); a.LastHeartbeat.After(stale) {
			a.LastHeartbeat = stale
		}
		a.CurrentTask = ""
	}
	b.log(now, actor, "", "server_stopped", "")
	_ = b.persistAndPublishLocked(now, &Event{Type: "shutdown"})
	b.mu.Unlock()
	err := b.Flush(context.Background())
	b.mu.Lock() // the deferred Unlock expects the lock held
	return err
}

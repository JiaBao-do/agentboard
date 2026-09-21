package agentboard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/JiaBao-do/agentboard/model"
)

// Store persists the whole board state. Implementations must be safe for
// concurrent use, although Board serialises its own calls.
type Store interface {
	// Load returns the persisted state, or an empty state if nothing has
	// been saved yet.
	Load() (*model.State, error)
	// Save durably replaces the persisted state.
	Save(*model.State) error
}

// MemStore is an in-memory Store, mainly for tests. It keeps a serialised
// copy so callers can never alias the stored state.
type MemStore struct {
	mu   sync.Mutex
	data []byte
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore { return &MemStore{} }

// Load implements Store.
func (m *MemStore) Load() (*model.State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return DecodeState(m.data)
}

// Save implements Store.
func (m *MemStore) Save(st *model.State) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.data = b
	m.mu.Unlock()
	return nil
}

// FileStore keeps the state in a single JSON file. Every Save writes a
// temporary file in the same directory, syncs it and renames it over the
// target, so a crash never leaves a half written file behind.
type FileStore struct {
	path string
	mu   sync.Mutex

	// beforeRename lets tests simulate a crash between writing the temporary
	// file and renaming it.
	beforeRename func() error
}

// NewFileStore returns a Store backed by the file at path. The file and its
// parent directory are created on the first Save.
func NewFileStore(path string) *FileStore { return &FileStore{path: path} }

// Load implements Store.
func (f *FileStore) Load() (*model.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return model.NewState(), nil
	}
	if err != nil {
		return nil, err
	}
	st, err := DecodeState(b)
	if err != nil {
		return nil, fmt.Errorf("agentboard: reading %s: %w", f.path, err)
	}
	return st, nil
}

// Save implements Store.
func (f *FileStore) Save(st *model.State) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(f.path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if f.beforeRename != nil {
		if err := f.beforeRename(); err != nil {
			return err
		}
	}
	return os.Rename(tmp.Name(), f.path)
}

// DecodeState parses a serialised State, migrates older schema versions to
// the current one and validates the result. It rejects data written by a
// newer schema version rather than guessing at it. An empty input yields an
// empty State.
func DecodeState(b []byte) (*model.State, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return model.NewState(), nil
	}
	st := &model.State{} // Version 0 unless the file says otherwise
	if err := json.Unmarshal(b, st); err != nil {
		return nil, err
	}
	if st.Version > model.SchemaVersion {
		return nil, fmt.Errorf("%w: data has schema version %d but this agentboard understands up to %d; upgrade agentboard",
			ErrInvalid, st.Version, model.SchemaVersion)
	}
	if st.Version < 0 {
		return nil, invalid("negative schema version %d", st.Version)
	}
	migrate(st)
	if st.Projects == nil {
		st.Projects = map[string]*model.Project{}
	}
	if st.Tasks == nil {
		st.Tasks = map[string]*model.Task{}
	}
	if st.Agents == nil {
		st.Agents = map[string]*model.Agent{}
	}
	if err := ValidateState(st); err != nil {
		return nil, err
	}
	return st, nil
}

// migrate upgrades st in place to model.SchemaVersion. Each step handles one
// version bump, so old files keep loading as the format grows.
func migrate(st *model.State) {
	if st.Version < 1 { // files that predate the version field
		for _, t := range st.Tasks {
			if t == nil {
				continue
			}
			if t.Type == "" {
				t.Type = model.KindTask
			}
			if t.Priority == "" {
				t.Priority = model.PriorityMedium
			}
		}
	}
	st.Version = model.SchemaVersion
}

// EncodeState writes st as indented JSON, the documented export format.
func EncodeState(w io.Writer, st *model.State) error {
	st.Version = model.SchemaVersion
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(st)
}

// ValidateState checks the internal consistency of st: keys match their
// values, every task belongs to a known project, statuses, priorities and
// kinds are valid, parents exist in the same project and sit at a higher
// level, sequence counters cover every task ID, and activity IDs strictly
// increase. Import uses it so a hand-edited file cannot corrupt a board.
func ValidateState(st *model.State) error {
	maxSeq := map[string]int{}
	for id, t := range st.Tasks {
		if t == nil || t.ID != id {
			return invalid("task key %q does not match its id", id)
		}
		if st.Projects[t.Project] == nil {
			return invalid("task %s refers to unknown project %q", id, t.Project)
		}
		if !t.Status.Valid() {
			return invalid("task %s has invalid status %q", id, t.Status)
		}
		if !t.Priority.Valid() {
			return invalid("task %s has invalid priority %q", id, t.Priority)
		}
		if !t.Type.Valid() {
			return invalid("task %s has invalid type %q", id, t.Type)
		}
		if _, n := splitID(id); n > maxSeq[t.Project] {
			maxSeq[t.Project] = n
		}
	}
	for id, t := range st.Tasks {
		if t.Parent == "" {
			continue
		}
		p := st.Tasks[t.Parent]
		switch {
		case p == nil:
			return invalid("task %s has unknown parent %q", id, t.Parent)
		case p.Project != t.Project:
			return invalid("task %s and its parent %s are in different projects", id, p.ID)
		case p.Type.Level() >= t.Type.Level():
			return invalid("task %s (%s) cannot have parent %s (%s)", id, t.Type, p.ID, p.Type)
		}
	}
	for key, p := range st.Projects {
		if p == nil || p.Key != key {
			return invalid("project key %q does not match its value", key)
		}
		if p.NextSeq < maxSeq[key] {
			return invalid("project %s next_seq %d is behind task %s-%d", key, p.NextSeq, key, maxSeq[key])
		}
	}
	for name, a := range st.Agents {
		if a == nil || a.Name != name {
			return invalid("agent key %q does not match its name", name)
		}
	}
	var prev int64
	for _, a := range st.Activity {
		if a.ID <= prev {
			return invalid("activity ids must strictly increase (%d after %d)", a.ID, prev)
		}
		prev = a.ID
	}
	if prev > st.NextActivityID {
		return invalid("next_activity_id %d is behind activity %d", st.NextActivityID, prev)
	}
	return nil
}

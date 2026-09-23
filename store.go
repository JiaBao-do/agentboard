package agentboard

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// Load implements Store. It reads the current format (see EncodeFile) and
// also the original plain JSON files: those are migrated on first load by
// keeping the old file once as <path>.bak and writing the new format.
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
	st, legacy, err := DecodeFile(b)
	if err != nil {
		return nil, fmt.Errorf("agentboard: reading %s: %w", f.path, err)
	}
	if legacy && len(bytes.TrimSpace(b)) > 0 {
		if err := f.migrateLocked(b, st); err != nil {
			return nil, fmt.Errorf("agentboard: migrating %s: %w", f.path, err)
		}
	}
	return st, nil
}

// migrateLocked keeps the legacy file as <path>.bak (never overwriting an
// existing backup, never deleting user data) and writes the new format.
func (f *FileStore) migrateLocked(old []byte, st *model.State) error {
	bak, err := os.OpenFile(f.path+".bak", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	switch {
	case err == nil:
		_, werr := bak.Write(old)
		if cerr := bak.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return werr
		}
	case !errors.Is(err, os.ErrExist):
		return err
	}
	return f.saveLocked(st)
}

// Save implements Store.
func (f *FileStore) Save(st *model.State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.saveLocked(st)
}

func (f *FileStore) saveLocked(st *model.State) error {
	b, err := EncodeFile(st)
	if err != nil {
		return err
	}
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

// ErrCorrupt is returned (wrapped) when a data file fails its length or
// checksum check, is truncated, or does not decompress.
var ErrCorrupt = errors.New("data file is corrupt")

// On-disk file format, version 2:
//
//	offset  size  field
//	0       4     magic "ABD2"
//	4       1     format version (2)
//	5       8     payload length, big endian
//	13      n     payload: compact JSON of the State, deflate compressed
//	13+n    4     CRC-32C (Castagnoli) of the payload, big endian
//
// Version 1 files were plain indented JSON (first byte '{').
const (
	fileMagic       = "ABD2"
	fileVersion     = 2
	fileHeaderLen   = 13
	maxDecompressed = 1 << 30 // refuse decompression bombs
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// EncodeFile serialises st in the on-disk format: compact JSON, deflated at
// the default level (measured: 28x smaller than indented JSON for a 1,000
// task board, see format_size_test.go), with a length and CRC-32C so damage
// is detected exactly. Encoding runs in the save queue's writer goroutine,
// never on the request path.
func EncodeFile(st *model.State) ([]byte, error) {
	st.Version = model.SchemaVersion
	raw, err := json.Marshal(st)
	if err != nil {
		return nil, err
	}
	var payload bytes.Buffer
	zw, err := flate.NewWriter(&payload, flate.DefaultCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	out := make([]byte, 0, fileHeaderLen+payload.Len()+4)
	out = append(out, fileMagic...)
	out = append(out, fileVersion)
	out = binary.BigEndian.AppendUint64(out, uint64(payload.Len()))
	out = append(out, payload.Bytes()...)
	out = binary.BigEndian.AppendUint32(out, crc32.Checksum(payload.Bytes(), crcTable))
	return out, nil
}

// DecodeFile parses a data file: the current format, or legacy plain JSON
// (legacy is then true so the caller can migrate it). Damage is reported as
// ErrCorrupt; a file written by a newer format is refused, never guessed at.
// It never panics on arbitrary input.
func DecodeFile(b []byte) (st *model.State, legacy bool, err error) {
	if !bytes.HasPrefix(b, []byte(fileMagic)) {
		if len(bytes.TrimSpace(b)) > 0 && bytes.HasPrefix(b, []byte("ABD")) {
			return nil, false, fmt.Errorf("%w: unknown file magic %q; upgrade agentboard", ErrInvalid, b[:4])
		}
		st, err = DecodeState(b)
		return st, true, err
	}
	if len(b) < fileHeaderLen+4 {
		return nil, false, fmt.Errorf("%w: truncated header (%d bytes)", ErrCorrupt, len(b))
	}
	if v := b[4]; v != fileVersion {
		return nil, false, fmt.Errorf("%w: data file format version %d but this agentboard understands %d; upgrade agentboard", ErrInvalid, v, fileVersion)
	}
	n := binary.BigEndian.Uint64(b[5:13])
	if n != uint64(len(b)-fileHeaderLen-4) {
		return nil, false, fmt.Errorf("%w: length says %d payload bytes, file has %d (truncated or appended to)", ErrCorrupt, n, len(b)-fileHeaderLen-4)
	}
	payload := b[fileHeaderLen : fileHeaderLen+int(n)]
	want := binary.BigEndian.Uint32(b[fileHeaderLen+int(n):])
	if got := crc32.Checksum(payload, crcTable); got != want {
		return nil, false, fmt.Errorf("%w: checksum mismatch (stored %08x, computed %08x)", ErrCorrupt, want, got)
	}
	zr := flate.NewReader(bytes.NewReader(payload))
	defer zr.Close()
	raw, err := io.ReadAll(io.LimitReader(zr, maxDecompressed+1))
	if err != nil {
		return nil, false, fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	if len(raw) > maxDecompressed {
		return nil, false, fmt.Errorf("%w: payload larger than %d bytes", ErrCorrupt, maxDecompressed)
	}
	st, err = DecodeState(raw)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	return st, false, nil
}

// stateKeys are the top-level JSON fields of a genuine State document (see
// model.State's json tags). Every one of them is written unconditionally by
// EncodeState/EncodeFile (none is `omitempty`), and every real legacy file
// predating the "version" field still named at least "projects" and "tasks"
// (see the fixtures in hierarchy_test.go). So a real export or legacy board
// file always has at least one of these keys at the top level.
var stateKeys = []string{"version", "projects", "tasks", "agents", "users", "activity", "next_activity_id", "archived_through"}

// looksLikeBoard reports (as a wrapped ErrInvalid) when b's top-level JSON
// object has none of stateKeys. That is the real gap behind a silent-wipe
// bug: a syntactically valid but unrelated JSON object (say {"hello":
// "world"}) unmarshals into a zero-valued State with zero tasks and zero
// projects, and ValidateState only checks internal consistency, so it finds
// nothing wrong with an empty board and lets it through. Checking for board
// shape first catches "not actually a board" before it is ever mistaken for
// "a genuine empty board".
func looksLikeBoard(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	for _, key := range stateKeys {
		if _, ok := raw[key]; ok {
			return nil
		}
	}
	return fmt.Errorf("%w: this does not look like an agentboard export or board file (no %v field present); "+
		"import wants the JSON produced by \"agentboard export\" or a real board.json", ErrInvalid, stateKeys)
}

// DecodeState parses a serialised State, migrates older schema versions to
// the current one and validates the result. It rejects data written by a
// newer schema version rather than guessing at it, and rejects JSON that
// does not have the shape of a board at all (see looksLikeBoard) rather than
// silently treating it as an empty one. An empty input yields an empty
// State.
func DecodeState(b []byte) (*model.State, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return model.NewState(), nil
	}
	if err := looksLikeBoard(b); err != nil {
		return nil, err
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
	if st.Users == nil {
		st.Users = map[string]*model.User{}
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
	// Version 2 added Users; older files simply lack the field, and the nil
	// map is initialised right after migrate() runs, so there is nothing to
	// transform here.
	// Version 3 added optional Task.StartDate/EndDate; older files simply
	// lack the fields, and a nil *Date is already the correct "no date"
	// zero value, so there is nothing to transform here either.
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
		if t.StartDate != nil && t.EndDate != nil && t.EndDate.Before(*t.StartDate) {
			return invalid("task %s has end date %s before start date %s", id, t.EndDate, t.StartDate)
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
	for email, u := range st.Users {
		if u == nil || u.Email != email {
			return invalid("user key %q does not match its email", email)
		}
		if email != strings.ToLower(email) {
			return invalid("user key %q must be lower case", email)
		}
		if len(u.PasswordHash) == 0 || len(u.Salt) == 0 || u.Iterations <= 0 {
			return invalid("user %q has an incomplete credential record", email)
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

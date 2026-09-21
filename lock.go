package agentboard

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// LockFileName is the single-instance lock inside a data directory.
const LockFileName = "agentboard.lock"

// ErrLocked means another live process holds the data directory.
var ErrLocked = errors.New("data directory is in use by another agentboard")

// LockInfo is the payload of the lock file: who holds the directory.
type LockInfo struct {
	PID     int       `json:"pid"`
	Host    string    `json:"host"`
	Started time.Time `json:"started"`
	Addr    string    `json:"addr,omitempty"` // the server's listen address, once known
	Token   string    `json:"token"`          // proves ownership on release
}

// Lock is a held data-directory lock: one writer per data dir. The lock is
// a file created with O_EXCL holding the owner's PID and host, so it works
// on every platform with the standard library alone. A crash leaves a stale
// file; the next start sees that its PID is dead and takes over. Release it
// with Release (also on SIGINT/SIGTERM and after the shutdown endpoint).
type Lock struct {
	path string
	info LockInfo
}

// AcquireLock takes the lock on dir (created if missing). If a live process
// on this host holds it, the error wraps ErrLocked and names the PID and
// address. If the recorded PID is dead the stale lock is replaced. A lock
// from another host, or an unreadable lock file, is never taken over
// automatically; see ForceUnlock.
func AcquireLock(dir string) (*Lock, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	tok := make([]byte, 8)
	_, _ = rand.Read(tok)
	l := &Lock{path: filepath.Join(dir, LockFileName), info: LockInfo{
		PID: os.Getpid(), Host: host, Started: time.Now().UTC(), Token: hex.EncodeToString(tok),
	}}
	for attempt := 0; attempt < 3; attempt++ {
		f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			werr := json.NewEncoder(f).Encode(l.info)
			if cerr := f.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				_ = os.Remove(l.path)
				return nil, werr
			}
			return l, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		held, herr := readLock(l.path)
		switch {
		case herr != nil && errors.Is(herr, os.ErrNotExist):
			continue // released in the meantime: retry
		case herr != nil:
			return nil, fmt.Errorf("%w: %s exists but cannot be read (%w); inspect it, or run with -force-unlock if you know no server is running", ErrLocked, l.path, herr)
		case held.Host != host:
			return nil, fmt.Errorf("%w: locked by pid %d on host %q (this host is %q); cannot tell whether it is alive", ErrLocked, held.PID, held.Host, host)
		case processAlive(held.PID):
			return nil, fmt.Errorf("%w: pid %d%s", ErrLocked, held.PID, addrSuffix(held.Addr))
		}
		// Stale: the recorded process is gone. Remove only that exact file.
		if cur, err := readLock(l.path); err == nil && cur.Token == held.Token {
			if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
	}
	return nil, fmt.Errorf("%w: could not take %s after retries", ErrLocked, l.path)
}

func addrSuffix(addr string) string {
	if addr == "" {
		return ""
	}
	return " listening on " + addr
}

func readLock(path string) (LockInfo, error) {
	var li LockInfo
	b, err := os.ReadFile(path)
	if err != nil {
		return li, err
	}
	if err := json.Unmarshal(b, &li); err != nil {
		return li, err
	}
	if li.PID <= 0 {
		return li, errors.New("no pid in lock file")
	}
	return li, nil
}

// ReadLock reports who holds dir, without taking it. The bool is false when
// nobody does (no lock file, or a stale one).
func ReadLock(dir string) (LockInfo, bool) {
	li, err := readLock(filepath.Join(dir, LockFileName))
	if err != nil {
		return li, false
	}
	host, _ := os.Hostname()
	if li.Host == host && !processAlive(li.PID) {
		return li, false
	}
	return li, true
}

// SetAddr records the listen address in the lock file, so a second start (or
// an offline command) can point at the running server.
func (l *Lock) SetAddr(addr string) error {
	l.info.Addr = addr
	b, err := json.Marshal(l.info)
	if err != nil {
		return err
	}
	tmp := l.path + ".tmp." + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

// Release removes the lock file if this Lock still owns it. It is safe to
// call more than once.
func (l *Lock) Release() error {
	cur, err := readLock(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || cur.Token != l.info.Token {
		return nil // not ours any more: leave it alone
	}
	if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ForceUnlock removes a lock file whose recorded process is provably dead
// (same host, PID not running). It refuses in every other case and never
// touches data files.
func ForceUnlock(dir string) error {
	path := filepath.Join(dir, LockFileName)
	li, err := readLock(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: lock file unreadable (%w); refusing to guess", ErrLocked, err)
	}
	host, _ := os.Hostname()
	if li.Host != host {
		return fmt.Errorf("%w: locked from host %q, cannot prove pid %d is dead", ErrLocked, li.Host, li.PID)
	}
	if processAlive(li.PID) {
		return fmt.Errorf("%w: pid %d is still running%s", ErrLocked, li.PID, addrSuffix(li.Addr))
	}
	return os.Remove(path)
}

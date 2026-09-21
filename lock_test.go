package agentboard_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JiaBao-do/agentboard"
)

// deadPID returns the PID of a process that has already exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.ProcessState.Pid()
}

func writeLock(t *testing.T, dir string, li agentboard.LockInfo) {
	t.Helper()
	b, _ := json.Marshal(li)
	if err := os.WriteFile(filepath.Join(dir, agentboard.LockFileName), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestSecondInstanceIsRefused(t *testing.T) {
	dir := t.TempDir()
	l, err := agentboard.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if err := l.SetAddr("127.0.0.1:7878"); err != nil {
		t.Fatal(err)
	}
	_, err = agentboard.AcquireLock(dir)
	if !errors.Is(err, agentboard.ErrLocked) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "pid "+itoa(os.Getpid())) || !strings.Contains(err.Error(), "127.0.0.1:7878") {
		t.Fatalf("the error must name the PID and address: %v", err)
	}
	if li, held := agentboard.ReadLock(dir); !held || li.PID != os.Getpid() || li.Addr != "127.0.0.1:7878" {
		t.Fatalf("ReadLock = %+v %v", li, held)
	}
}

func TestStaleLockIsRecovered(t *testing.T) {
	dir := t.TempDir()
	host, _ := os.Hostname()
	writeLock(t, dir, agentboard.LockInfo{PID: deadPID(t), Host: host, Token: "old", Started: time.Now()})
	if _, held := agentboard.ReadLock(dir); held {
		t.Fatal("a dead owner must not count as holding the lock")
	}
	l, err := agentboard.AcquireLock(dir)
	if err != nil {
		t.Fatalf("stale lock not recovered: %v", err)
	}
	defer l.Release()
	if li, _ := agentboard.ReadLock(dir); li.PID != os.Getpid() {
		t.Fatalf("lock not taken over: %+v", li)
	}
}

func TestLockRefusalsThatMustNotAutoRecover(t *testing.T) {
	host, _ := os.Hostname()
	tests := []struct {
		name string
		put  func(dir string)
	}{
		{"other host", func(dir string) {
			writeLock(t, dir, agentboard.LockInfo{PID: 1, Host: host + "-elsewhere", Token: "x"})
		}},
		{"unreadable lock file", func(dir string) {
			os.WriteFile(filepath.Join(dir, agentboard.LockFileName), []byte("garbage"), 0o600)
		}},
		{"live pid", func(dir string) {
			writeLock(t, dir, agentboard.LockInfo{PID: os.Getpid(), Host: host, Token: "x"})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.put(dir)
			if _, err := agentboard.AcquireLock(dir); !errors.Is(err, agentboard.ErrLocked) {
				t.Fatalf("err = %v", err)
			}
			if err := agentboard.ForceUnlock(dir); err == nil {
				t.Fatal("ForceUnlock must refuse when the owner is not provably dead")
			}
			if _, err := os.Stat(filepath.Join(dir, agentboard.LockFileName)); err != nil {
				t.Fatal("the lock file must survive a refusal")
			}
		})
	}
}

func TestForceUnlockOnlyWhenProvablyDeadAndKeepsData(t *testing.T) {
	dir := t.TempDir()
	host, _ := os.Hostname()
	data := filepath.Join(dir, "board.json")
	os.WriteFile(data, []byte("precious"), 0o600)
	writeLock(t, dir, agentboard.LockInfo{PID: deadPID(t), Host: host, Token: "x"})
	if err := agentboard.ForceUnlock(dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(data); string(b) != "precious" {
		t.Fatal("ForceUnlock touched the data file")
	}
	if err := agentboard.ForceUnlock(dir); err != nil { // nothing to do is fine
		t.Fatal(err)
	}
}

func TestReleaseOnlyRemovesOwnLock(t *testing.T) {
	dir := t.TempDir()
	l, _ := agentboard.AcquireLock(dir)
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil { // idempotent
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, agentboard.LockFileName)); !os.IsNotExist(err) {
		t.Fatal("lock file left behind")
	}
	// Someone else took it after we lost it: our Release must leave theirs.
	l1, _ := agentboard.AcquireLock(dir)
	os.Remove(filepath.Join(dir, agentboard.LockFileName))
	l2, _ := agentboard.AcquireLock(dir)
	l1.Release()
	if _, held := agentboard.ReadLock(dir); !held {
		t.Fatal("Release removed a lock owned by someone else")
	}
	l2.Release()
}

func TestConcurrentAcquireHasOneWinner(t *testing.T) {
	dir := t.TempDir()
	var wins atomic.Int32
	var wg sync.WaitGroup
	var mu sync.Mutex
	var locks []*agentboard.Lock
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l, err := agentboard.AcquireLock(dir); err == nil {
				wins.Add(1)
				mu.Lock()
				locks = append(locks, l)
				mu.Unlock()
			} else if !errors.Is(err, agentboard.ErrLocked) {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d winners", wins.Load())
	}
	for _, l := range locks {
		l.Release()
	}
}

func TestLockOutlivesTheBoard(t *testing.T) {
	dir := t.TempDir()
	l, _ := agentboard.AcquireLock(dir)
	defer l.Release()
	b, err := agentboard.Open(agentboard.Options{Store: agentboard.NewFileStore(filepath.Join(dir, "board.json"))})
	if err != nil {
		t.Fatal(err)
	}
	b.Close(context.Background())
	if _, err := agentboard.AcquireLock(dir); !errors.Is(err, agentboard.ErrLocked) {
		t.Fatal("the lock must outlive the board")
	}
}

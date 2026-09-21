package agentboard_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JiaBao-do/agentboard"
	"github.com/JiaBao-do/agentboard/model"
)

// spyStore wraps a MemStore, counts saves, can fail the first N of them,
// validates every saved snapshot and announces each successful save.
type spyStore struct {
	inner  *agentboard.MemStore
	saves  atomic.Int64
	failN  atomic.Int64 // number of upcoming saves to fail
	saved  chan struct{}
	badErr atomic.Value // first ValidateState error, if any
}

func newSpyStore() *spyStore {
	return &spyStore{inner: agentboard.NewMemStore(), saved: make(chan struct{}, 1024)}
}

func (s *spyStore) Load() (*model.State, error) { return s.inner.Load() }

func (s *spyStore) Save(st *model.State) error {
	if s.failN.Add(-1) >= 0 {
		return errors.New("disk on fire")
	}
	s.failN.Store(0)
	if err := agentboard.ValidateState(st); err != nil {
		s.badErr.CompareAndSwap(nil, err)
	}
	s.saves.Add(1)
	err := s.inner.Save(st)
	select {
	case s.saved <- struct{}{}:
	default:
	}
	return err
}

func openBoard(t *testing.T, o agentboard.Options) *agentboard.Board {
	t.Helper()
	b, err := agentboard.Open(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	return b
}

func TestAsyncSavesAreCoalesced(t *testing.T) {
	st := newSpyStore()
	b := openBoard(t, agentboard.Options{Store: st, SaveDebounce: time.Hour, SaveMaxLatency: 2 * time.Hour})
	b.CreateProject("AB", "n", "")
	for i := range 200 {
		if _, err := b.AddTask(agentboard.NewTask{Project: "AB", Title: fmt.Sprint("t", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if n := st.saves.Load(); n != 0 {
		t.Fatalf("saved %d times before any flush", n)
	}
	if s := b.SaveStatus(); !s.Dirty || s.State() != "saving" {
		t.Fatalf("status = %+v", s)
	}
	if err := b.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := st.saves.Load(); n != 1 {
		t.Fatalf("201 changes produced %d saves, want 1", n)
	}
	if s := b.SaveStatus(); s.Dirty || s.State() != "saved" || s.LastSave.IsZero() || s.Saves != 1 {
		t.Fatalf("status after flush = %+v", s)
	}
	if err := b.Flush(context.Background()); err != nil || st.saves.Load() != 1 {
		t.Fatal("a clean flush must not save again")
	}
}

func TestMaxLatencyCapsContinuousLoad(t *testing.T) {
	st := newSpyStore()
	// The debounce alone would never fire: a change arrives every 5ms, far
	// more often than the 200ms window. Only the cap can force a save.
	b := openBoard(t, agentboard.Options{Store: st, SaveDebounce: 200 * time.Millisecond, SaveMaxLatency: 400 * time.Millisecond})
	b.CreateProject("AB", "n", "")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				b.AddTask(agentboard.NewTask{Project: "AB", Title: "load"})
			}
		}
	}()
	select {
	case <-st.saved:
	case <-time.After(10 * time.Second):
		t.Error("continuous activity starved the writer: no save within 10s")
	}
	close(stop)
	wg.Wait()
}

func TestCloseFlushesEverything(t *testing.T) {
	st := newSpyStore()
	b, err := agentboard.Open(agentboard.Options{Store: st, SaveDebounce: time.Hour, SaveMaxLatency: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	b.CreateProject("AB", "n", "")
	b.AddTask(agentboard.NewTask{Project: "AB", Title: "kept"})
	if err := b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(context.Background()); err != nil { // idempotent
		t.Fatal(err)
	}
	b2 := openBoard(t, agentboard.Options{Store: st})
	if got := b2.Tasks(agentboard.Filter{}); len(got) != 1 || got[0].Title != "kept" {
		t.Fatalf("reloaded tasks = %+v", got)
	}
	// Changes after Close are saved inline instead of being lost.
	if _, err := b.AddTask(agentboard.NewTask{Project: "AB", Title: "late"}); err != nil {
		t.Fatal(err)
	}
	if got := openBoard(t, agentboard.Options{Store: st}).Tasks(agentboard.Filter{}); len(got) != 2 {
		t.Fatalf("post-close change lost: %d tasks", len(got))
	}
}

func TestSaveFailureIsRetriedAndSurfaced(t *testing.T) {
	st := newSpyStore()
	st.failN.Store(2)
	b := openBoard(t, agentboard.Options{Store: st, SaveDebounce: time.Millisecond, SaveMaxLatency: 5 * time.Millisecond})
	b.CreateProject("AB", "n", "")
	// The first two attempts fail; the writer keeps the change pending and
	// retries with backoff until one succeeds.
	select {
	case <-st.saved:
	case <-time.After(10 * time.Second):
		t.Fatal("writer never recovered")
	}
	s := b.SaveStatus()
	if s.Failures != 2 || s.Saves != 1 || s.Dirty || s.LastError != "" || s.State() != "saved" {
		t.Fatalf("status after recovery = %+v", s)
	}
	if got := openBoard(t, agentboard.Options{Store: st}).Projects(); len(got) != 1 {
		t.Fatalf("saved state lacks the project: %+v", got)
	}
}

func TestSaveFailureIsVisibleWhileFailing(t *testing.T) {
	st := newSpyStore()
	st.failN.Store(1 << 30)
	b := openBoard(t, agentboard.Options{Store: st, SaveDebounce: time.Millisecond, SaveMaxLatency: 5 * time.Millisecond})
	b.CreateProject("AB", "n", "")
	err := b.Flush(context.Background())
	if err == nil {
		t.Fatal("Flush must return the save error")
	}
	s := b.SaveStatus()
	if !s.Dirty || s.LastError == "" || s.State() != "failed" || s.Failures < 1 {
		t.Fatalf("status = %+v", s)
	}
	if snap := b.Snapshot(); snap.Save.State() != "failed" {
		t.Fatalf("snapshot must carry the save status: %+v", snap.Save)
	}
	st.failN.Store(0)
	if err := b.Flush(context.Background()); err != nil {
		t.Fatalf("flush after recovery: %v", err)
	}
	if s := b.SaveStatus(); s.Dirty || s.LastError != "" {
		t.Fatalf("status after recovery = %+v", s)
	}
}

func TestBoardLeavesNoGoroutines(t *testing.T) {
	settle := func() int {
		for range 100 {
			runtime.Gosched()
			time.Sleep(10 * time.Millisecond)
		}
		return runtime.NumGoroutine()
	}
	before := settle()
	for range 5 {
		b, err := agentboard.Open(agentboard.Options{Store: newSpyStore(), SaveDebounce: time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		ch, cancel := b.Subscribe()
		b.CreateProject("AB", "n", "")
		<-ch
		cancel()
		if err := b.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if after := settle(); after > before {
		buf := make([]byte, 1<<16)
		t.Fatalf("goroutines: %d before, %d after\n%s", before, after, buf[:runtime.Stack(buf, true)])
	}
}

func TestSnapshotsAreConsistentUnderConcurrentMutation(t *testing.T) {
	st := newSpyStore()
	b := openBoard(t, agentboard.Options{Store: st, SaveDebounce: time.Millisecond, SaveMaxLatency: 5 * time.Millisecond})
	b.CreateProject("AB", "n", "")
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			agent := fmt.Sprint("agent", w)
			for i := range 40 {
				task, err := b.AddTask(agentboard.NewTask{Project: "AB", Title: fmt.Sprint("t", w, "-", i)})
				if err != nil {
					t.Error(err)
					return
				}
				b.Claim(task.ID, agent, time.Minute)
				b.Comment(task.ID, agent, "working")
				b.Heartbeat(agent, agentboard.HeartbeatRequest{Task: task.ID, Meta: map[string]string{"i": fmt.Sprint(i)}})
				b.Complete(task.ID, agent)
			}
		}()
	}
	// Readers and subscribers hammer the board at the same time.
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			ch, cancel := b.Subscribe()
			defer cancel()
			for {
				select {
				case <-stop:
					return
				case <-ch:
				default:
					b.Snapshot()
					b.Tasks(agentboard.Filter{})
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	readers.Wait()
	if err := b.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err, _ := st.badErr.Load().(error); err != nil {
		t.Fatalf("a saved snapshot was inconsistent: %v", err)
	}
	got := openBoard(t, agentboard.Options{Store: st}).Tasks(agentboard.Filter{Status: agentboard.StatusDone})
	if len(got) != 8*40 {
		t.Fatalf("done tasks after reload = %d, want %d", len(got), 8*40)
	}
}

func TestSyncModeSavesInline(t *testing.T) {
	st := newSpyStore()
	b := openBoard(t, agentboard.Options{Store: st, SaveMode: agentboard.SaveSync})
	b.CreateProject("AB", "n", "")
	b.AddTask(agentboard.NewTask{Project: "AB", Title: "a"})
	b.AddTask(agentboard.NewTask{Project: "AB", Title: "b"})
	if n := st.saves.Load(); n != 3 {
		t.Fatalf("sync mode saved %d times for 3 changes", n)
	}
	// No flush needed: a second board sees everything at once.
	if got := openBoard(t, agentboard.Options{Store: st}).Tasks(agentboard.Filter{}); len(got) != 2 {
		t.Fatalf("tasks = %d", len(got))
	}
	if s := b.SaveStatus(); s.Mode != "sync" || s.Dirty {
		t.Fatalf("status = %+v", s)
	}
	// A failing disk is reported to the caller in sync mode.
	st.failN.Store(1)
	if _, err := b.AddTask(agentboard.NewTask{Project: "AB", Title: "c"}); err == nil {
		t.Fatal("sync mode must return the save error")
	}
	if s := b.SaveStatus(); !s.Dirty || s.LastError == "" {
		t.Fatalf("status after failure = %+v", s)
	}
}

func TestInvalidSaveMode(t *testing.T) {
	if _, err := agentboard.Open(agentboard.Options{SaveMode: "eventually"}); !errors.Is(err, agentboard.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestCrashLosesAtMostTheWindow(t *testing.T) {
	st := newSpyStore()
	b, err := agentboard.Open(agentboard.Options{Store: st, SaveDebounce: time.Hour, SaveMaxLatency: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	b.CreateProject("AB", "n", "")
	b.AddTask(agentboard.NewTask{Project: "AB", Title: "unsaved"})
	// "kill -9": nothing flushes. A board opened on the same store sees only
	// what already reached it, which is the documented loss window.
	crashed := openBoard(t, agentboard.Options{Store: st})
	if n := len(crashed.Tasks(agentboard.Filter{})); n != 0 {
		t.Fatalf("saw %d tasks that were never saved", n)
	}
	// A graceful stop loses nothing.
	if err := b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(openBoard(t, agentboard.Options{Store: st}).Tasks(agentboard.Filter{})); n != 1 {
		t.Fatalf("graceful close lost data: %d tasks", n)
	}
}

func TestSaveEventsReachSubscribers(t *testing.T) {
	st := newSpyStore()
	b := openBoard(t, agentboard.Options{Store: st, SaveDebounce: time.Millisecond, SaveMaxLatency: 5 * time.Millisecond})
	ch, cancel := b.Subscribe()
	defer cancel()
	b.CreateProject("AB", "n", "")
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == "save" {
				return
			}
		case <-deadline:
			t.Fatal("no save event")
		}
	}
}

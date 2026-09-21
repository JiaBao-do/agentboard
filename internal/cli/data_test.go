package cli_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JiaBao-do/agentboard"
)

func seedBoard(t *testing.T, base string) {
	t.Helper()
	env := envOf(map[string]string{"AGENTBOARD_URL": base, "AGENTBOARD_AGENT": "seeder"})
	for _, args := range [][]string{
		{"project", "add", "AB", "n"},
		{"task", "add", "-p", "AB", "first"},
		{"task", "add", "-p", "AB", "second"},
		{"task", "claim", "AB-1"},
	} {
		if code, _, errw := run(t, env, args...); code != 0 {
			t.Fatalf("%v: %s", args, errw)
		}
	}
}

func lockPath(dir string) string { return filepath.Join(dir, agentboard.LockFileName) }

func TestSecondServeOnTheSameDirIsRefused(t *testing.T) {
	dir := t.TempDir()
	base, stop := startServe(t, dir)
	defer stop()
	code, _, errw := run(t, noEnv, "serve", "-addr", "127.0.0.1:0", "-data", dir)
	if code != 1 || !strings.Contains(errw, "in use") || !strings.Contains(errw, "pid ") || !strings.Contains(errw, strings.TrimPrefix(base, "http://")) {
		t.Fatalf("code=%d err=%q", code, errw)
	}
}

func TestLockReleasedOnGracefulStopAndOnUIStop(t *testing.T) {
	dir := t.TempDir()
	_, stop := startServe(t, dir)
	if _, err := os.Stat(lockPath(dir)); err != nil {
		t.Fatalf("no lock while running: %v", err)
	}
	if code := stop(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if _, err := os.Stat(lockPath(dir)); !os.IsNotExist(err) {
		t.Fatal("lock still there after a graceful stop")
	}
	// The shutdown endpoint (the UI Stop button) releases it too.
	base, stop2 := startServe(t, dir)
	if code, _, errw := run(t, envOf(map[string]string{"AGENTBOARD_URL": base}), "stop"); code != 0 {
		t.Fatal(errw)
	}
	stop2()
	if _, err := os.Stat(lockPath(dir)); !os.IsNotExist(err) {
		t.Fatal("lock still there after the shutdown endpoint")
	}
}

func TestStaleLockDoesNotBlockStart(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	host, _ := os.Hostname()
	b, _ := json.Marshal(agentboard.LockInfo{PID: cmd.ProcessState.Pid(), Host: host, Token: "crashed"})
	os.WriteFile(lockPath(dir), b, 0o600)
	_, stop := startServe(t, dir)
	defer stop()
}

func TestOfflineCommandsRefuseWhileServerRuns(t *testing.T) {
	dir := t.TempDir()
	base, stop := startServe(t, dir)
	defer stop()
	seedBoard(t, base)
	out := filepath.Join(t.TempDir(), "x.json")
	os.WriteFile(out, []byte(`{}`), 0o600)
	for _, args := range [][]string{
		{"export", "-data", dir},
		{"dump", "-data", dir},
		{"import", out, "-data", dir},
	} {
		code, _, errw := run(t, noEnv, args...)
		if code != 1 || !strings.Contains(errw, "in use") || !strings.Contains(errw, "talk to it instead") {
			t.Errorf("%v: code=%d err=%q", args, code, errw)
		}
	}
	// ...but the API path works while the server runs.
	code, dumped, errw := run(t, envOf(map[string]string{"AGENTBOARD_URL": base}), "dump")
	if code != 0 || !strings.Contains(dumped, `"first"`) {
		t.Fatalf("online dump: %d %q %q", code, dumped, errw)
	}
	if _, err := os.Stat(lockPath(dir)); err != nil {
		t.Fatal("a refused offline command must not disturb the server's lock")
	}
}

func TestExportImportRoundTripAcrossMachines(t *testing.T) {
	src := t.TempDir()
	base, stop := startServe(t, src)
	seedBoard(t, base)
	tmp := t.TempDir()
	plain, gz := filepath.Join(tmp, "b.json"), filepath.Join(tmp, "b.json.gz")
	env := envOf(map[string]string{"AGENTBOARD_URL": base})
	if code, _, errw := run(t, env, "export", "-o", plain); code != 0 {
		t.Fatal(errw)
	}
	if code, _, errw := run(t, env, "export", "-o", gz, "-gzip"); code != 0 {
		t.Fatal(errw)
	}
	stop() // graceful: everything flushed

	// Offline export straight from the data dir (server is stopped).
	offline := filepath.Join(tmp, "offline.json")
	if code, _, errw := run(t, noEnv, "export", "-data", src, "-o", offline, "-with-archive"); code != 0 {
		t.Fatal(errw)
	}
	if _, err := os.Stat(lockPath(src)); !os.IsNotExist(err) {
		t.Fatal("an offline command must release the lock")
	}

	want := readState(t, plain)
	for name, file := range map[string]string{"plain": plain, "gzip": gz, "offline": offline} {
		t.Run(name, func(t *testing.T) {
			dst := t.TempDir()
			code, out, errw := run(t, noEnv, "import", file, "-data", dst)
			if code != 0 || !strings.Contains(out, "imported 2 tasks") {
				t.Fatalf("import: %d %q %q", code, out, errw)
			}
			got, err := agentboard.NewFileStore(filepath.Join(dst, "board.json")).Load()
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Tasks) != len(want.Tasks) || got.Tasks["AB-1"].Assignee != "seeder" {
				t.Fatalf("imported board differs: %+v", got.Tasks["AB-1"])
			}
			// The offline export was taken after the graceful stop, which logs one more entry.
			if extra := got.NextActivityID - want.NextActivityID; (name == "offline") != (extra == 1) || extra > 1 {
				t.Fatalf("activity counter %d vs %d", got.NextActivityID, want.NextActivityID)
			}
			if _, err := os.Stat(lockPath(dst)); !os.IsNotExist(err) {
				t.Fatal("import left its lock behind")
			}
		})
	}
	t.Run("import keeps the previous data", func(t *testing.T) {
		dst := t.TempDir()
		os.WriteFile(filepath.Join(dst, "board.json"), []byte(`{"version":1,"projects":{},"tasks":{},"agents":{}}`), 0o600)
		code, _, errw := run(t, noEnv, "import", plain, "-data", dst)
		if code != 0 || !strings.Contains(errw, "previous data kept") {
			t.Fatalf("%d %q", code, errw)
		}
		kept, _ := filepath.Glob(filepath.Join(dst, "board.json.before-import-*"))
		if len(kept) != 1 {
			t.Fatalf("kept files = %v", kept)
		}
	})
	t.Run("invalid input is refused before touching anything", func(t *testing.T) {
		dst := t.TempDir()
		bad := filepath.Join(tmp, "bad.json")
		os.WriteFile(bad, []byte(`{"version":1,"tasks":{"X-1":{"id":"X-1","project":"NOPE","status":"todo","priority":"low","type":"task"}}}`), 0o600)
		code, _, errw := run(t, noEnv, "import", bad, "-data", dst)
		if code != 1 || !strings.Contains(errw, "not a valid board") {
			t.Fatalf("%d %q", code, errw)
		}
		if _, err := os.Stat(filepath.Join(dst, "board.json")); !os.IsNotExist(err) {
			t.Fatal("a rejected import wrote a data file")
		}
	})
	t.Run("usage", func(t *testing.T) {
		if code, _, _ := run(t, noEnv, "import"); code != 2 {
			t.Fatalf("code %d", code)
		}
	})
}

func readState(t *testing.T, path string) *agentboard.State {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 2 && raw[0] == 0x1f {
		zr, _ := gzip.NewReader(bytes.NewReader(raw))
		raw, _ = io.ReadAll(zr)
	}
	st, _, err := agentboard.DecodeFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestWithArchiveNeedsData(t *testing.T) {
	if code, _, errw := run(t, noEnv, "export", "-with-archive"); code != 2 || !strings.Contains(errw, "-data") {
		t.Fatalf("%d %q", code, errw)
	}
}

func TestForceUnlockFlagRefusesLiveOwner(t *testing.T) {
	dir := t.TempDir()
	_, stop := startServe(t, dir)
	defer stop()
	code, _, errw := run(t, noEnv, "serve", "-addr", "127.0.0.1:0", "-data", dir, "-force-unlock")
	if code != 1 || !strings.Contains(errw, "still running") {
		t.Fatalf("code=%d err=%q", code, errw)
	}
	if _, err := os.Stat(lockPath(dir)); err != nil {
		t.Fatal("force-unlock removed a live server's lock")
	}
}

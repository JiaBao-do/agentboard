package cli_test

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// registerUser registers a human account directly through the HTTP API
// (there is no CLI command for it - registration is a web-UI/API action).
func registerUser(t *testing.T, base, email, password string) {
	t.Helper()
	body := `{"email":"` + email + `","password":"` + password + `"}`
	resp, err := http.Post(base+"/api/auth/register", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("register: status %d", resp.StatusCode)
	}
}

// TestExportCredentialsOnlyOverTheOfflinePath is the CLI-level regression
// test for AGENTBOARD-11: "agentboard export"/"dump" without -data go over
// the network (GET /api/export) and must never contain a registered user's
// password_hash/salt, while "export -data DIR" reads board.json straight
// off disk (already needs filesystem access to the data directory) and
// must keep full fidelity, or restoring a board with its accounts would be
// impossible.
func TestExportCredentialsOnlyOverTheOfflinePath(t *testing.T) {
	dir := t.TempDir()
	base, stop := startServe(t, dir)
	seedBoard(t, base)
	registerUser(t, base, "dev@example.com", "correct horse battery staple")

	env := envOf(map[string]string{"AGENTBOARD_URL": base})
	for _, cmd := range []string{"export", "dump"} {
		t.Run("online "+cmd, func(t *testing.T) {
			code, out, errw := run(t, env, cmd)
			if code != 0 {
				t.Fatal(errw)
			}
			// password_hash/salt must never appear in a network export; the
			// activity log still names the registered email against its own
			// "user_registered" entry (pre-existing attribution history, not
			// a User record - see the root package's export_security_test.go).
			for _, secret := range []string{"password_hash", "salt"} {
				if strings.Contains(out, secret) {
					t.Fatalf("online %s leaked %q:\n%s", cmd, secret, out)
				}
			}
			if !strings.Contains(out, `"users": {}`) {
				t.Fatalf("online %s did not omit the users map:\n%s", cmd, out)
			}
		})
	}

	stop() // graceful: flushes to disk before the offline read below

	t.Run("offline export -data keeps full fidelity", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "offline.json")
		if code, _, errw := run(t, noEnv, "export", "-data", dir, "-o", out); code != 0 {
			t.Fatal(errw)
		}
		raw, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		if !strings.Contains(body, "password_hash") || !strings.Contains(body, "\"salt\"") {
			t.Fatalf("offline export dropped credentials, restore-with-accounts would be impossible:\n%s", body)
		}
		if !strings.Contains(body, "dev@example.com") {
			t.Fatalf("offline export dropped the registered user entirely:\n%s", body)
		}
	})
}

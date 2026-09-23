package agentboard_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JiaBao-do/agentboard"
)

// TestNetworkExportNeverCarriesCredentials is the regression test for
// AGENTBOARD-11: GET /api/export (and Board.Export, which it calls) used to
// marshal the whole internal State unfiltered, including every User's raw
// PasswordHash and Salt - exactly the input an offline dictionary/
// brute-force attack needs, and reachable with no authentication at all on
// the default loopback bind. This asserts the fix at both the HTTP layer
// (the literal bytes a client receives) and the Go API layer (Board.Export
// itself), with a real registered user present so the test would have
// failed before the fix.
func TestNetworkExportNeverCarriesCredentials(t *testing.T) {
	ts, b := newServer(t, agentboard.ServerOptions{})

	if _, err := b.Register("dev@example.com", "correct horse battery staple"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	t.Run("HTTP GET /api/export", func(t *testing.T) {
		resp, body := do(t, "GET", ts.URL+"/api/export", "", nil)
		if resp.StatusCode != 200 {
			t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
		}
		for _, secret := range []string{"password_hash", "salt", "iterations"} {
			if strings.Contains(body, secret) {
				t.Fatalf("network export response contains %q:\n%s", secret, body)
			}
		}
		// The registered user's account record must not be present at all
		// over this path (see Export's doc comment): a network export omits
		// Users entirely rather than emit a half-shaped User record that
		// ValidateState would then refuse to re-import. This does not touch
		// the activity log, which still names "dev@example.com" against its
		// own "user_registered" entry the same way it already names every
		// other actor for every other action - that is pre-existing,
		// unrelated attribution history (see docs/PITFALLS.md #5), not a
		// User record, and out of scope for this fix.
		var decoded struct {
			Users map[string]json.RawMessage `json:"users"`
		}
		if err := json.Unmarshal([]byte(body), &decoded); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(decoded.Users) != 0 {
			t.Fatalf("users = %v, want empty", decoded.Users)
		}
	})

	t.Run("Board.Export (Go API, same code path the HTTP handler uses)", func(t *testing.T) {
		st, err := b.Export()
		if err != nil {
			t.Fatal(err)
		}
		if len(st.Users) != 0 {
			t.Fatalf("Users = %v, want empty", st.Users)
		}
		raw, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "password_hash") || strings.Contains(string(raw), "salt") {
			t.Fatalf("Board.Export result contains credential material: %s", raw)
		}
		// The redacted export must still be a valid, re-importable board
		// (with the caveat, documented in docs/DATA_FORMAT.md, that
		// accounts are gone and must be re-registered).
		if err := agentboard.ValidateState(st); err != nil {
			t.Fatalf("Export's result does not validate: %v", err)
		}
	})

	t.Run("agentboard dump goes through the same handler, same result", func(t *testing.T) {
		resp, body := do(t, "GET", ts.URL+"/api/export", "", nil) // dump uses the identical client.Export call
		if resp.StatusCode != 200 || strings.Contains(body, "password_hash") {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
	})

	t.Run("ExportWithCredentials keeps full fidelity for the offline path", func(t *testing.T) {
		st, err := b.ExportWithCredentials()
		if err != nil {
			t.Fatal(err)
		}
		u := st.Users["dev@example.com"]
		if u == nil || len(u.PasswordHash) == 0 || len(u.Salt) == 0 || u.Iterations <= 0 {
			t.Fatalf("ExportWithCredentials dropped credentials: %+v", u)
		}
		if err := agentboard.ValidateState(st); err != nil {
			t.Fatalf("full-fidelity export does not validate: %v", err)
		}
	})
}

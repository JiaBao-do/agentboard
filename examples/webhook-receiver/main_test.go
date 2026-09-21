package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JiaBao-do/agentboard"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func TestReceiverVerifiesAndPrintsEvents(t *testing.T) {
	var out lockedBuf
	recv := httptest.NewServer(handler("s3cret", &out))
	defer recv.Close()

	board, _ := agentboard.Open(agentboard.Options{SaveMode: agentboard.SaveSync})
	defer board.Close(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	wait := (&agentboard.Webhook{URL: recv.URL, Secret: "s3cret"}).Start(ctx, board)

	board.CreateProject("AB", "n", "demo")
	board.AddTask(agentboard.NewTask{Actor: "demo", Project: "AB", Title: "t"})
	board.Claim("AB-1", "worker-1", time.Minute)

	// The payload carries the task as it is when delivered, not when the
	// event happened, so the AddTask event may already show the claim.
	// Three events arrive, in order, and the last one shows the final state.
	deadline := time.After(10 * time.Second)
	for strings.Count(out.String(), "\n") < 3 {
		select {
		case <-deadline:
			t.Fatalf("got:\n%s", out.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if lines[0] != "event=project" || !strings.HasPrefix(lines[1], "event=task task=AB-1 ") ||
		lines[2] != "event=task task=AB-1 status=in_progress agent=worker-1" {
		t.Fatalf("got:\n%s", out.String())
	}
	cancel()
	wait()
}

func TestReceiverRejectsBadSignatures(t *testing.T) {
	var out lockedBuf
	h := handler("s3cret", &out)
	body := `{"event":{"type":"task"}}`
	for name, sig := range map[string]string{
		"missing":   "",
		"wrong key": agentboard.SignWebhookBody("other", []byte(body)),
	} {
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		if sig != "" {
			req.Header.Set("X-Agentboard-Signature", sig)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
	if out.String() != "" {
		t.Fatalf("unverified events were printed: %q", out.String())
	}
	good := httptest.NewRequest("POST", "/", strings.NewReader(body))
	good.Header.Set("X-Agentboard-Signature", agentboard.SignWebhookBody("s3cret", []byte(body)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, good)
	if rec.Code != http.StatusNoContent || out.String() != "event=task\n" {
		t.Fatalf("status %d out %q", rec.Code, out.String())
	}
}

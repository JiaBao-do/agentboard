package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/JiaBao-do/agentboard"
)

func TestOutputMatchesExpected(t *testing.T) {
	board, err := agentboard.Open(agentboard.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer board.Close(context.Background())
	ts := httptest.NewServer(agentboard.NewServer(agentboard.ServerOptions{Board: board}).Handler())
	defer ts.Close()

	var out bytes.Buffer
	if err := run(context.Background(), &out, ts.URL); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("expected_output.txt")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != strings.TrimSpace(strings.ReplaceAll(string(want), "\r\n", "\n")) {
		t.Fatalf("output differs from expected_output.txt:\n%s", out.String())
	}
}

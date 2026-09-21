package agentboard_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/JiaBao-do/agentboard"
)

// BenchmarkBurst compares persistence modes under a burst of updates against
// a real file store: async coalesces the burst into a few writes, sync writes
// once per change.
func BenchmarkBurst(b *testing.B) {
	for _, mode := range []agentboard.SaveMode{agentboard.SaveAsync, agentboard.SaveSync} {
		b.Run(string(mode), func(b *testing.B) {
			board, err := agentboard.Open(agentboard.Options{
				Store:    agentboard.NewFileStore(filepath.Join(b.TempDir(), "board.json")),
				SaveMode: mode,
			})
			if err != nil {
				b.Fatal(err)
			}
			board.CreateProject("AB", "bench", "tester")
			b.ReportAllocs()
			for b.Loop() {
				if _, err := board.AddTask(agentboard.NewTask{Actor: "tester", Project: "AB", Title: "burst"}); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			if err := board.Close(context.Background()); err != nil {
				b.Fatal(err)
			}
		})
	}
}

func BenchmarkClaimRelease(b *testing.B) {
	board, _ := agentboard.Open(agentboard.Options{})
	defer board.Close(context.Background())
	board.CreateProject("AB", "bench", "tester")
	task, _ := board.AddTask(agentboard.NewTask{Actor: "tester", Project: "AB", Title: "t"})
	b.ReportAllocs()
	for b.Loop() {
		if _, err := board.Claim(task.ID, "agent", 0); err != nil {
			b.Fatal(err)
		}
		if _, err := board.Release(task.ID, "agent"); err != nil {
			b.Fatal(err)
		}
	}
}

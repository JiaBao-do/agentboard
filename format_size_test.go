package agentboard_test

import (
	"bytes"
	"compress/flate"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/JiaBao-do/agentboard/model"
)

// seededState builds a realistic board: n tasks with ~5 activity entries each.
func seededState(n int) *model.State {
	st := model.NewState()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st.Projects["AB"] = &model.Project{Key: "AB", Name: "Agent Board", NextSeq: n, CreatedAt: base}
	agents := []string{"batchx-builder", "collectx-builder", "routex-builder", "quartzx-builder"}
	for _, a := range agents {
		st.Agents[a] = &model.Agent{Name: a, Kind: "claude-code", LastHeartbeat: base, Meta: map[string]string{"role": "builder"}}
	}
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("AB-%d", i)
		who := agents[i%len(agents)]
		st.Tasks[id] = &model.Task{
			ID: id, Project: "AB", Type: model.KindTask, Title: fmt.Sprintf("Implement feature number %d for the loop", i),
			Description: "Write the implementation, tests and docs, then push through the hook.",
			Status:      model.StatusDone, Priority: model.PriorityMedium, Labels: []string{"go", "loop"},
			Assignee: who, CreatedBy: who, UpdatedBy: who, CreatedAt: base, UpdatedAt: base.Add(time.Hour),
		}
		for _, act := range []string{"created", "claimed", "status", "comment", "done"} {
			st.NextActivityID++
			st.Activity = append(st.Activity, model.Activity{
				ID: st.NextActivityID, Time: base.Add(time.Duration(st.NextActivityID) * time.Second),
				Actor: who, TaskID: id, Action: act, Detail: "lease 10m0s",
			})
		}
	}
	return st
}

// TestReportFormatSizes prints the on-disk size of each candidate encoding
// (run with -v). It documents why the file format is compact JSON + flate.
func TestReportFormatSizes(t *testing.T) {
	for _, n := range []int{1000, 10000} {
		st := seededState(n)
		pretty, _ := json.MarshalIndent(st, "", "  ")
		compact, _ := json.Marshal(st)
		var gb bytes.Buffer
		_ = gob.NewEncoder(&gb).Encode(st)
		z := func(b []byte, level int) (int, time.Duration) {
			start := time.Now()
			var out bytes.Buffer
			w, _ := flate.NewWriter(&out, level)
			w.Write(b)
			w.Close()
			return out.Len(), time.Since(start)
		}
		t.Logf("%d tasks, %d activity entries", n, len(st.Activity))
		t.Logf("  indented json      %9d bytes", len(pretty))
		t.Logf("  compact json       %9d bytes", len(compact))
		t.Logf("  gob                %9d bytes", gb.Len())
		for _, lv := range []struct {
			name  string
			level int
		}{{"BestSpeed", flate.BestSpeed}, {"Default", flate.DefaultCompression}, {"Best", flate.BestCompression}} {
			s, d := z(compact, lv.level)
			t.Logf("  json+flate %-9s %9d bytes  %v", lv.name, s, d.Round(time.Millisecond))
		}
		s, d := z(gb.Bytes(), flate.BestSpeed)
		t.Logf("  gob+flate BestSpeed %8d bytes  %v", s, d.Round(time.Millisecond))
	}
}

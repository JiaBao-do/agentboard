package model_test

import (
	"encoding/json"
	"testing"

	"github.com/JiaBao-do/agentboard/model"
)

func TestStatus(t *testing.T) {
	tests := []struct {
		s     model.Status
		valid bool
		label string
	}{
		{model.StatusTodo, true, "To do"},
		{model.StatusInProgress, true, "In progress"},
		{model.StatusReview, true, "Review"},
		{model.StatusDone, true, "Done"},
		{model.StatusBlocked, true, "Blocked"},
		{"", false, ""},
		{"nope", false, "nope"},
	}
	for _, tc := range tests {
		if tc.s.Valid() != tc.valid || tc.s.Label() != tc.label {
			t.Errorf("%q: valid=%v label=%q", tc.s, tc.s.Valid(), tc.s.Label())
		}
	}
	if len(model.Statuses) != 5 || model.Statuses[0] != model.StatusTodo {
		t.Fatalf("Statuses = %v", model.Statuses)
	}
}

func TestPriority(t *testing.T) {
	tests := []struct {
		p     model.Priority
		valid bool
		rank  int
	}{
		{model.PriorityLow, true, 0},
		{model.PriorityMedium, true, 1},
		{model.PriorityHigh, true, 2},
		{model.PriorityUrgent, true, 3},
		{"", false, 0},
		{"asap", false, 0},
	}
	for _, tc := range tests {
		if tc.p.Valid() != tc.valid || tc.p.Rank() != tc.rank {
			t.Errorf("%q: valid=%v rank=%d", tc.p, tc.p.Valid(), tc.p.Rank())
		}
	}
}

func TestKind(t *testing.T) {
	tests := []struct {
		k     model.Kind
		valid bool
		level int
	}{
		{model.KindEpic, true, 0},
		{model.KindStory, true, 1},
		{model.KindTask, true, 2},
		{"", false, 2}, // unknown counts as a task
		{"bug", false, 2},
	}
	for _, tc := range tests {
		if tc.k.Valid() != tc.valid || tc.k.Level() != tc.level {
			t.Errorf("%q: valid=%v level=%d", tc.k, tc.k.Valid(), tc.k.Level())
		}
	}
}

func TestNewStateIsUsableAndVersioned(t *testing.T) {
	st := model.NewState()
	if st.Version != model.SchemaVersion || st.Projects == nil || st.Tasks == nil || st.Agents == nil {
		t.Fatalf("NewState = %+v", st)
	}
}

func TestJSONFieldNamesAreStable(t *testing.T) {
	// These names are the documented wire and file format; renaming one is a
	// breaking change that needs a schema version bump.
	b, err := json.Marshal(model.Task{ID: "AB-1", Type: model.KindTask, Parent: "AB-0"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	for _, key := range []string{"id", "project", "type", "parent", "title", "status", "priority", "labels", "created_by", "created_at", "updated_at"} {
		if _, ok := m[key]; !ok {
			t.Errorf("task JSON lacks %q: %s", key, b)
		}
	}
}

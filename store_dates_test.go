package agentboard_test

import (
	"strings"
	"testing"

	"github.com/JiaBao-do/agentboard"
	"github.com/JiaBao-do/agentboard/model"
)

// TestDecodeStateMigratesV2BoardsWithoutDates checks the schema version 2 ->
// 3 migration added for Task.StartDate/EndDate (AGENTBOARD-6): a genuine
// pre-dates board file (version 2, tasks with no "start_date"/"end_date"
// keys at all, as every real file saved before this change looks) must
// decode to schema version 3 with StartDate/EndDate left nil, not be
// rejected and not panic.
func TestDecodeStateMigratesV2BoardsWithoutDates(t *testing.T) {
	const v2 = `{"version":2,"projects":{"AB":{"key":"AB","name":"n","next_seq":1}},` +
		`"tasks":{"AB-1":{"id":"AB-1","project":"AB","type":"task","title":"t","status":"todo","priority":"low"}},` +
		`"agents":{},"users":{},"activity":[],"next_activity_id":0}`

	st, err := agentboard.DecodeState([]byte(v2))
	if err != nil {
		t.Fatalf("DecodeState: %v", err)
	}
	if st.Version != model.SchemaVersion {
		t.Fatalf("version = %d, want migrated to %d", st.Version, model.SchemaVersion)
	}
	task := st.Tasks["AB-1"]
	if task == nil {
		t.Fatal("task AB-1 missing after migration")
	}
	if task.StartDate != nil || task.EndDate != nil {
		t.Fatalf("migrated task has dates = %+v/%+v, want both nil", task.StartDate, task.EndDate)
	}

	// The migrated state must itself still validate and round-trip through
	// EncodeState/DecodeState.
	if err := agentboard.ValidateState(st); err != nil {
		t.Fatalf("migrated state does not validate: %v", err)
	}
	var buf strings.Builder
	if err := agentboard.EncodeState(&buf, st); err != nil {
		t.Fatalf("EncodeState: %v", err)
	}
	if strings.Contains(buf.String(), `"start_date"`) || strings.Contains(buf.String(), `"end_date"`) {
		t.Fatal("re-encoded state should omit unset date fields (omitempty)")
	}
	back, err := agentboard.DecodeState([]byte(buf.String()))
	if err != nil {
		t.Fatalf("DecodeState of re-encoded state: %v", err)
	}
	if back.Version != model.SchemaVersion || back.Tasks["AB-1"].StartDate != nil {
		t.Fatalf("round trip changed shape: %+v", back.Tasks["AB-1"])
	}
}

// TestDecodeStateWithGenuineDates checks that tasks carrying real start/end
// dates decode, validate and round-trip byte-exact through the file format.
func TestDecodeStateWithGenuineDates(t *testing.T) {
	st := model.NewState()
	st.Projects["AB"] = &model.Project{Key: "AB", Name: "n", NextSeq: 1}
	start := model.NewDate(2026, 3, 2)
	end := model.NewDate(2026, 3, 9)
	st.Tasks["AB-1"] = &model.Task{
		ID: "AB-1", Project: "AB", Type: model.KindTask, Title: "t",
		Status: model.StatusTodo, Priority: model.PriorityLow,
		StartDate: &start, EndDate: &end,
	}
	if err := agentboard.ValidateState(st); err != nil {
		t.Fatalf("ValidateState: %v", err)
	}
	b, err := agentboard.EncodeFile(st)
	if err != nil {
		t.Fatalf("EncodeFile: %v", err)
	}
	back, _, err := agentboard.DecodeFile(b)
	if err != nil {
		t.Fatalf("DecodeFile: %v", err)
	}
	got := back.Tasks["AB-1"]
	if got.StartDate == nil || got.StartDate.String() != "2026-03-02" {
		t.Fatalf("StartDate = %v, want 2026-03-02", got.StartDate)
	}
	if got.EndDate == nil || got.EndDate.String() != "2026-03-09" {
		t.Fatalf("EndDate = %v, want 2026-03-09", got.EndDate)
	}
}

// TestValidateStateRejectsEndBeforeStart guards the schema invariant
// (documented on model.Task): a hand-edited or imported file with
// EndDate < StartDate must never validate, the same way other cross-field
// invariants (parent level, activity ID ordering) are rejected.
func TestValidateStateRejectsEndBeforeStart(t *testing.T) {
	st := model.NewState()
	st.Projects["AB"] = &model.Project{Key: "AB", Name: "n", NextSeq: 1}
	start := model.NewDate(2026, 3, 9)
	end := model.NewDate(2026, 3, 2) // before start
	st.Tasks["AB-1"] = &model.Task{
		ID: "AB-1", Project: "AB", Type: model.KindTask, Title: "t",
		Status: model.StatusTodo, Priority: model.PriorityLow,
		StartDate: &start, EndDate: &end,
	}
	if err := agentboard.ValidateState(st); err == nil {
		t.Fatal("expected ValidateState to reject end date before start date")
	}
}

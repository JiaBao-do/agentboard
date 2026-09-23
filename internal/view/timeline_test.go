package view_test

import (
	"testing"

	"github.com/JiaBao-do/agentboard/internal/view"
	"github.com/JiaBao-do/agentboard/model"
)

func d(s string) model.Date {
	dt, err := model.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return dt
}

func TestDefaultWindow(t *testing.T) {
	start, end := view.DefaultWindow(d("2026-03-17"))
	if start.String() != "2026-03-01" {
		t.Fatalf("start = %s, want 2026-03-01", start)
	}
	if end.String() != "2026-06-01" { // 3 months later
		t.Fatalf("end = %s, want 2026-06-01", end)
	}
}

func TestShiftWindow(t *testing.T) {
	start, end := view.DefaultWindow(d("2026-03-17"))
	ns, ne := view.ShiftWindow(start, end, 1)
	if ns.String() != "2026-04-01" || ne.String() != "2026-07-01" {
		t.Fatalf("shift +1 = %s..%s", ns, ne)
	}
	ps, pe := view.ShiftWindow(start, end, -1)
	if ps.String() != "2026-02-01" || pe.String() != "2026-05-01" {
		t.Fatalf("shift -1 = %s..%s", ps, pe)
	}
}

func TestWeekStart(t *testing.T) {
	tests := []struct{ in, want string }{
		{"2026-03-02", "2026-03-02"}, // Monday itself
		{"2026-03-03", "2026-03-02"}, // Tuesday
		{"2026-03-08", "2026-03-02"}, // Sunday
		{"2026-03-01", "2026-02-23"}, // Sunday, previous week's Monday
	}
	for _, tc := range tests {
		if got := view.WeekStart(d(tc.in)); got.String() != tc.want {
			t.Errorf("WeekStart(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestDaysBetween(t *testing.T) {
	if got := view.DaysBetween(d("2026-03-01"), d("2026-03-09")); got != 8 {
		t.Fatalf("DaysBetween = %d, want 8", got)
	}
	if got := view.DaysBetween(d("2026-03-09"), d("2026-03-01")); got != -8 {
		t.Fatalf("DaysBetween reversed = %d, want -8", got)
	}
	if got := view.DaysBetween(d("2026-03-01"), d("2026-03-01")); got != 0 {
		t.Fatalf("DaysBetween same = %d, want 0", got)
	}
}

// TestMonthsInWindowMatchesUserExample checks the exact scenario the user
// pasted onto AGENTBOARD-6: month columns Mar/Apr/May 2026, each with
// weekly gridlines. March's Mondays are 2, 9, 16, 23, 30; April's are 6, 13,
// 20, 27; May's are 4, 11, 18, 25.
func TestMonthsInWindowMatchesUserExample(t *testing.T) {
	start, end := d("2026-03-01"), d("2026-06-01")
	months := view.MonthsInWindow(start, end)
	if len(months) != 3 {
		t.Fatalf("months = %d, want 3: %+v", len(months), months)
	}
	wantLabels := []string{"Mar 2026", "Apr 2026", "May 2026"}
	wantTicks := [][]string{
		{"2026-03-02", "2026-03-09", "2026-03-16", "2026-03-23", "2026-03-30"},
		{"2026-04-06", "2026-04-13", "2026-04-20", "2026-04-27"},
		{"2026-05-04", "2026-05-11", "2026-05-18", "2026-05-25"},
	}
	for i, m := range months {
		if m.Label != wantLabels[i] {
			t.Errorf("month %d label = %q, want %q", i, m.Label, wantLabels[i])
		}
		if len(m.WeekTicks) != len(wantTicks[i]) {
			t.Fatalf("month %d ticks = %v, want %v", i, ticksToStrings(m.WeekTicks), wantTicks[i])
		}
		for j, wt := range m.WeekTicks {
			if wt.String() != wantTicks[i][j] {
				t.Errorf("month %d tick %d = %s, want %s", i, j, wt, wantTicks[i][j])
			}
		}
	}
	if months[0].Start.String() != "2026-03-01" {
		t.Errorf("first month start = %s, want 2026-03-01", months[0].Start)
	}
	if months[2].End.String() != "2026-06-01" {
		t.Errorf("last month end = %s, want 2026-06-01", months[2].End)
	}
}

func ticksToStrings(ds []model.Date) []string {
	out := make([]string, len(ds))
	for i, x := range ds {
		out[i] = x.String()
	}
	return out
}

func TestMonthsInWindowClipsPartialMonths(t *testing.T) {
	// A window starting mid-month must clip the first segment's Start to the
	// window, not to the 1st of that month.
	months := view.MonthsInWindow(d("2026-03-15"), d("2026-04-10"))
	if len(months) != 2 {
		t.Fatalf("months = %d, want 2", len(months))
	}
	if months[0].Start.String() != "2026-03-15" || months[0].End.String() != "2026-04-01" {
		t.Errorf("month 0 = %s..%s", months[0].Start, months[0].End)
	}
	if months[1].Start.String() != "2026-04-01" || months[1].End.String() != "2026-04-10" {
		t.Errorf("month 1 = %s..%s", months[1].Start, months[1].End)
	}
}

func TestMonthsInWindowEmptyOrInverted(t *testing.T) {
	if got := view.MonthsInWindow(d("2026-03-01"), d("2026-03-01")); got != nil {
		t.Errorf("empty window = %v, want nil", got)
	}
	if got := view.MonthsInWindow(d("2026-04-01"), d("2026-03-01")); got != nil {
		t.Errorf("inverted window = %v, want nil", got)
	}
}

func TestFraction(t *testing.T) {
	win0, win1 := d("2026-03-01"), d("2026-03-11") // 10 day window
	tests := []struct {
		in   string
		want float64
	}{
		{"2026-03-01", 0},
		{"2026-03-06", 0.5},
		{"2026-03-11", 1},
		{"2026-02-20", 0}, // before the window: clamped
		{"2026-04-01", 1}, // after the window: clamped
	}
	for _, tc := range tests {
		if got := view.Fraction(d(tc.in), win0, win1); got != tc.want {
			t.Errorf("Fraction(%s) = %v, want %v", tc.in, got, tc.want)
		}
	}
	if got := view.Fraction(d("2026-03-05"), win0, win0); got != 0 {
		t.Errorf("zero width window = %v, want 0", got)
	}
}

func taskWithRange(id, start, end string) model.Task {
	t := model.Task{ID: id}
	if start != "" {
		s := d(start)
		t.StartDate = &s
	}
	if end != "" {
		e := d(end)
		t.EndDate = &e
	}
	return t
}

func TestTaskRange(t *testing.T) {
	if _, _, ok := view.TaskRange(taskWithRange("AB-1", "", "")); ok {
		t.Error("no dates should not be ok")
	}
	if _, _, ok := view.TaskRange(taskWithRange("AB-1", "2026-03-01", "")); ok {
		t.Error("start only should not be ok")
	}
	if _, _, ok := view.TaskRange(taskWithRange("AB-1", "", "2026-03-01")); ok {
		t.Error("end only should not be ok")
	}
	start, end, ok := view.TaskRange(taskWithRange("AB-1", "2026-03-01", "2026-03-01"))
	if !ok {
		t.Fatal("single-day range should be ok")
	}
	if start.String() != "2026-03-01" || end.String() != "2026-03-02" {
		t.Errorf("single day range = %s..%s, want end exclusive one day later", start, end)
	}
}

func TestBarSpan(t *testing.T) {
	winStart, winEnd := d("2026-03-01"), d("2026-04-01") // 31 day window
	tests := []struct {
		name        string
		task        model.Task
		wantOK      bool
		left, width float64
	}{
		{"fully inside", taskWithRange("AB-1", "2026-03-01", "2026-03-10"), true, 0, 10.0 / 31},
		{"undated", taskWithRange("AB-2", "", ""), false, 0, 0},
		{"entirely before window", taskWithRange("AB-3", "2026-01-01", "2026-01-05"), false, 0, 0},
		{"entirely after window", taskWithRange("AB-4", "2026-05-01", "2026-05-05"), false, 0, 0},
		{"starts before, ends inside (clamped left)", taskWithRange("AB-5", "2026-02-20", "2026-03-05"), true, 0, 5.0 / 31},
		{"starts inside, ends after (clamped right)", taskWithRange("AB-6", "2026-03-25", "2026-04-10"), true, 24.0 / 31, 1 - 24.0/31},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			left, width, ok := view.BarSpan(winStart, winEnd, tc.task)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if abs(left-tc.left) > 1e-9 || abs(width-tc.width) > 1e-9 {
				t.Errorf("left/width = %v/%v, want %v/%v", left, width, tc.left, tc.width)
			}
			if left < 0 || left > 1 || left+width > 1+1e-9 {
				t.Errorf("bar escapes [0,1]: left=%v width=%v", left, width)
			}
		})
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func TestTimelineRowsOmitsUndatedAndOutOfWindow(t *testing.T) {
	tasks := []model.Task{
		taskWithRange("AB-1", "2026-03-01", "2026-03-05"),
		{ID: "AB-2", Title: "undated"},                    // no dates at all
		taskWithRange("AB-3", "2026-01-01", "2026-01-05"), // outside window
	}
	rows := view.TimelineRows(tasks, d("2026-03-01"), d("2026-04-01"))
	if len(rows) != 1 || rows[0].Task.ID != "AB-1" {
		t.Fatalf("rows = %+v, want only AB-1", rows)
	}
}

func TestSortByStartDate(t *testing.T) {
	tasks := []model.Task{
		taskWithRange("AB-3", "2026-03-10", "2026-03-12"),
		{ID: "AB-9", Title: "undated"},
		taskWithRange("AB-1", "2026-03-01", "2026-03-05"),
		taskWithRange("AB-2", "2026-03-01", "2026-03-09"), // same start as AB-1, tie-broken by ID
	}
	got := view.SortByStartDate(tasks)
	want := []string{"AB-1", "AB-2", "AB-3"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %+v", len(got), len(want), got)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("position %d = %s, want %s", i, got[i].ID, id)
		}
	}
	// input untouched
	if tasks[0].ID != "AB-3" {
		t.Error("SortByStartDate mutated its input")
	}
}

func TestWindowLabel(t *testing.T) {
	tests := []struct {
		start, end, want string
	}{
		{"2026-03-01", "2026-04-01", "Mar 2026"},
		{"2026-03-01", "2026-06-01", "Mar - May 2026"},
		{"2026-12-01", "2027-02-01", "Dec 2026 - Jan 2027"},
	}
	for _, tc := range tests {
		if got := view.WindowLabel(d(tc.start), d(tc.end)); got != tc.want {
			t.Errorf("WindowLabel(%s,%s) = %q, want %q", tc.start, tc.end, got, tc.want)
		}
	}
}

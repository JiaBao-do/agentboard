package view

import (
	"fmt"
	"sort"

	"github.com/JiaBao-do/agentboard/model"
)

// DefaultTimelineMonths is how many calendar months the Timeline view shows
// by default: enough to see a typical phase plan (the user's own example
// showed three: Mar/Apr/May) without loading the whole project history at
// once.
const DefaultTimelineMonths = 3

// DefaultWindow returns a sensible default Timeline window anchored on
// today: the first day of today's month through DefaultTimelineMonths
// months later (end exclusive), so "today" always starts inside the first
// visible month rather than at its edge.
func DefaultWindow(today model.Date) (start, end model.Date) {
	start = model.NewDate(today.Year(), today.Month(), 1)
	end = start.AddMonths(DefaultTimelineMonths)
	return start, end
}

// ShiftWindow moves a [start, end) window by months calendar months,
// keeping its width, for the Timeline view's prev/next navigation (months
// negative moves back).
func ShiftWindow(start, end model.Date, months int) (model.Date, model.Date) {
	return start.AddMonths(months), end.AddMonths(months)
}

// WeekStart returns the Monday on or before d (ISO week start), used to
// place the Timeline's weekly gridlines.
func WeekStart(d model.Date) model.Date {
	wd := int(d.Weekday()) // Sunday=0 .. Saturday=6
	offset := (wd + 6) % 7 // Monday=0 .. Sunday=6
	return d.AddDays(-offset)
}

// DaysBetween returns the number of days from a to b (b - a); negative if b
// is before a. Both are calendar dates (no time-of-day), so this is always
// an exact integer number of days, immune to DST arithmetic surprises.
func DaysBetween(a, b model.Date) int {
	return int(b.Sub(a.Time).Hours() / 24)
}

// Month is one calendar month segment of a Timeline header, clipped to the
// requested window: Start/End may fall mid-month at the window's own edges.
type Month struct {
	Label string // e.g. "Mar 2026"
	Start model.Date
	End   model.Date // exclusive
	// WeekTicks are the Mondays within [Start, End) that fall strictly after
	// Start, i.e. the internal weekly gridlines drawn under this month's
	// label (a tick exactly on Start would coincide with the month's own
	// left edge and adds nothing to draw).
	WeekTicks []model.Date
}

// MonthsInWindow splits [start, end) into one Month per calendar month it
// touches, each clipped to the window, in order. end is exclusive. An empty
// or inverted window (end <= start) returns nil.
func MonthsInWindow(start, end model.Date) []Month {
	if !start.Before(end) {
		return nil
	}
	var out []Month
	cur := start
	for cur.Before(end) {
		monthStart := model.NewDate(cur.Year(), cur.Month(), 1)
		monthEnd := monthStart.AddMonths(1)
		segStart, segEnd := cur, monthEnd
		if segEnd.After(end) {
			segEnd = end
		}
		m := Month{
			Label: cur.Format("Jan 2006"),
			Start: segStart,
			End:   segEnd,
		}
		for w := WeekStart(segStart); w.Before(segEnd); w = w.AddDays(7) {
			if w.After(segStart) {
				m.WeekTicks = append(m.WeekTicks, w)
			}
		}
		out = append(out, m)
		cur = monthEnd
	}
	return out
}

// Fraction returns how far d sits between winStart and winEnd, as a value
// clamped to [0, 1]. A zero-width window (winEnd <= winStart) returns 0.
func Fraction(d, winStart, winEnd model.Date) float64 {
	total := DaysBetween(winStart, winEnd)
	if total <= 0 {
		return 0
	}
	f := float64(DaysBetween(winStart, d)) / float64(total)
	switch {
	case f < 0:
		return 0
	case f > 1:
		return 1
	}
	return f
}

// TaskRange returns a task's display range for the Timeline view: [start,
// endExclusive), where endExclusive is EndDate plus one day so the end date
// itself renders as a full day-wide segment of the bar, not a zero-width
// sliver. ok is false when the task has no StartDate or no EndDate (an
// undated task; the Timeline view omits it, see AGENTBOARD-6's spec) or
// when EndDate is before StartDate (should not happen - Board.Update and
// ValidateState both reject it - but TaskRange never trusts that and simply
// reports not ok rather than panicking or drawing something backwards).
func TaskRange(t model.Task) (start, endExclusive model.Date, ok bool) {
	if t.StartDate == nil || t.EndDate == nil {
		return model.Date{}, model.Date{}, false
	}
	if t.EndDate.Before(*t.StartDate) {
		return model.Date{}, model.Date{}, false
	}
	return *t.StartDate, t.EndDate.AddDays(1), true
}

// BarSpan computes a task's Gantt bar within [winStart, winEnd) as left/width
// fractions of the window, both in [0, 1], clamped so a bar that only
// partially overlaps the window is drawn truncated at the edge rather than
// running off it. ok is false when the task is undated (see TaskRange) or
// its range does not overlap the window at all, in which case left/width
// are meaningless and the caller should omit the row's bar entirely.
func BarSpan(winStart, winEnd model.Date, t model.Task) (left, width float64, ok bool) {
	start, end, ok := TaskRange(t)
	if !ok {
		return 0, 0, false
	}
	if !start.Before(winEnd) || !end.After(winStart) {
		return 0, 0, false // no overlap with the window at all
	}
	left = Fraction(start, winStart, winEnd)
	right := Fraction(end, winStart, winEnd)
	width = right - left
	if width <= 0 {
		return 0, 0, false
	}
	return left, width, true
}

// HasTimelineRange reports whether t has both a StartDate and an EndDate,
// i.e. whether the Timeline view would draw a row for it.
func HasTimelineRange(t model.Task) bool {
	return t.StartDate != nil && t.EndDate != nil
}

// WindowLabel renders a [start, end) window for display, e.g. "Mar 2026 -
// May 2026" or, within one month, just that month.
func WindowLabel(start, end model.Date) string {
	lastMonth := end.AddDays(-1)
	if start.Year() == lastMonth.Year() && start.Month() == lastMonth.Month() {
		return start.Format("Jan 2006")
	}
	if start.Year() == lastMonth.Year() {
		return fmt.Sprintf("%s - %s", start.Format("Jan"), lastMonth.Format("Jan 2006"))
	}
	return fmt.Sprintf("%s - %s", start.Format("Jan 2006"), lastMonth.Format("Jan 2006"))
}

// TimelineRow is one dated task laid out for the Timeline view.
type TimelineRow struct {
	Task        model.Task
	Left, Width float64 // fractions of the window, both in [0, 1]
}

// TimelineRows returns one TimelineRow per task in tasks that has both a
// start and an end date and overlaps [winStart, winEnd), in the given task
// order (callers typically pre-sort, e.g. by StartDate). Undated tasks and
// tasks entirely outside the window are omitted, never zero-width rows.
func TimelineRows(tasks []model.Task, winStart, winEnd model.Date) []TimelineRow {
	var out []TimelineRow
	for _, t := range tasks {
		left, width, ok := BarSpan(winStart, winEnd, t)
		if !ok {
			continue
		}
		out = append(out, TimelineRow{Task: t, Left: left, Width: width})
	}
	return out
}

// SortByStartDate returns a copy of tasks with a start and end date, ordered
// by StartDate (earliest first), then by ID for a stable tie-break.
// Undated tasks are dropped, the same rule TimelineRows applies.
func SortByStartDate(tasks []model.Task) []model.Task {
	out := make([]model.Task, 0, len(tasks))
	for _, t := range tasks {
		if HasTimelineRange(t) {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].StartDate.Equal(out[j].StartDate.Time) {
			return out[i].StartDate.Before(*out[j].StartDate)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

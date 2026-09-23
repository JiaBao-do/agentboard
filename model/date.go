package model

import (
	"encoding/json"
	"fmt"
	"time"
)

// dateLayout is the wire and CLI format for a Date: "2006-01-02".
const dateLayout = "2006-01-02"

// Date is a calendar date: year, month, day, with no time-of-day and no
// time zone. It is used for planning fields such as Task.StartDate and
// Task.EndDate, which describe when something happens on a calendar (a
// Gantt/timeline bar), not a specific instant. The zero Date is not a valid
// calendar date; use IsZero to test for "unset" the way callers already do
// for time.Time.
//
// Date marshals to and parses from JSON as a plain "YYYY-MM-DD" string
// (never an object, never a full RFC3339 timestamp), so it reads the same
// on the wire, in board.json and on the command line.
type Date struct {
	// Time always has hour/minute/second/nanosecond zero and Location UTC;
	// only Year/Month/Day are meaningful. Exported so callers can use the
	// full time.Time API (Before, After, AddDate, Format, ...) without a
	// forwarding method for every one of them.
	time.Time
}

// NewDate returns the Date for the given calendar day.
func NewDate(year int, month time.Month, day int) Date {
	return Date{time.Date(year, month, day, 0, 0, 0, 0, time.UTC)}
}

// DateOf truncates t to its calendar date in UTC, discarding time-of-day
// and any other zone's offset.
func DateOf(t time.Time) Date {
	y, m, d := t.UTC().Date()
	return NewDate(y, m, d)
}

// ParseDate parses s as "YYYY-MM-DD". An empty string is not a valid date;
// callers that want "no date" use a nil *Date instead.
func ParseDate(s string) (Date, error) {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return Date{}, fmt.Errorf("date %q must be YYYY-MM-DD: %w", s, err)
	}
	return Date{t}, nil
}

// String renders d as "YYYY-MM-DD" ("" for the zero Date).
func (d Date) String() string {
	if d.IsZero() {
		return ""
	}
	return d.Format(dateLayout)
}

// Before reports whether d is before o (calendar order).
func (d Date) Before(o Date) bool { return d.Time.Before(o.Time) }

// After reports whether d is after o (calendar order).
func (d Date) After(o Date) bool { return d.Time.After(o.Time) }

// AddDays returns the date n days after d (n may be negative).
func (d Date) AddDays(n int) Date { return Date{d.AddDate(0, 0, n)} }

// AddMonths returns the date n calendar months after d (n may be negative).
func (d Date) AddMonths(n int) Date { return Date{d.AddDate(0, n, 0)} }

// MarshalJSON implements json.Marshaler, writing d as "YYYY-MM-DD".
func (d Date) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// UnmarshalJSON implements json.Unmarshaler, accepting a "YYYY-MM-DD"
// string. An empty string unmarshals to the zero Date, matching how an
// omitted field behaves; callers that need to distinguish "absent" from
// "present but empty" should use a *Date field (as Task does) rather than
// relying on IsZero of an always-present one.
func (d *Date) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("date must be a JSON string: %w", err)
	}
	if s == "" {
		d.Time = time.Time{}
		return nil
	}
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return fmt.Errorf("date %q must be YYYY-MM-DD: %w", s, err)
	}
	d.Time = t
	return nil
}

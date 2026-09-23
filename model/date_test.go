package model_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/JiaBao-do/agentboard/model"
)

func TestDateParseAndString(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"2026-03-02", "2026-03-02", false},
		{"2026-12-31", "2026-12-31", false},
		{"", "", true},
		{"03/02/2026", "", true},
		{"2026-13-01", "", true}, // invalid month
		{"not-a-date", "", true},
	}
	for _, tc := range tests {
		d, err := model.ParseDate(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseDate(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && d.String() != tc.want {
			t.Errorf("ParseDate(%q).String() = %q, want %q", tc.in, d.String(), tc.want)
		}
	}
}

func TestDateZeroValue(t *testing.T) {
	var d model.Date
	if !d.IsZero() {
		t.Fatal("zero Date should be IsZero")
	}
	if d.String() != "" {
		t.Fatalf("zero Date.String() = %q, want empty", d.String())
	}
}

func TestDateJSONRoundTrip(t *testing.T) {
	d := model.NewDate(2026, time.March, 2)
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"2026-03-02"` {
		t.Fatalf("Marshal = %s, want \"2026-03-02\"", b)
	}
	var back model.Date
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Equal(d.Time) {
		t.Fatalf("round trip = %v, want %v", back, d)
	}
}

func TestDateUnmarshalEmptyIsZero(t *testing.T) {
	var d model.Date
	if err := json.Unmarshal([]byte(`""`), &d); err != nil {
		t.Fatal(err)
	}
	if !d.IsZero() {
		t.Fatalf("unmarshal of \"\" = %v, want zero", d)
	}
}

func TestDateUnmarshalRejectsBadFormat(t *testing.T) {
	var d model.Date
	if err := json.Unmarshal([]byte(`"03/02/2026"`), &d); err == nil {
		t.Fatal("expected an error for a non-ISO date")
	}
	if err := json.Unmarshal([]byte(`42`), &d); err == nil {
		t.Fatal("expected an error for a non-string JSON value")
	}
}

func TestDateOf(t *testing.T) {
	ts := time.Date(2026, time.March, 2, 23, 59, 59, 0, time.UTC)
	if got := model.DateOf(ts); got.String() != "2026-03-02" {
		t.Fatalf("DateOf = %q, want 2026-03-02", got.String())
	}
	// A non-UTC zone that crosses midnight UTC still resolves to its own
	// calendar day once normalized to UTC.
	loc := time.FixedZone("test", -8*3600) // UTC-8
	ts2 := time.Date(2026, time.March, 2, 23, 0, 0, 0, loc)
	if got := model.DateOf(ts2); got.String() != "2026-03-03" {
		t.Fatalf("DateOf across zones = %q, want 2026-03-03", got.String())
	}
}

func TestDateBeforeAfterAdd(t *testing.T) {
	a := model.NewDate(2026, time.March, 2)
	b := model.NewDate(2026, time.March, 9)
	if !a.Before(b) || b.Before(a) {
		t.Fatal("Before wrong")
	}
	if !b.After(a) || a.After(b) {
		t.Fatal("After wrong")
	}
	if got := a.AddDays(7); !got.Equal(b.Time) {
		t.Fatalf("AddDays(7) = %v, want %v", got, b)
	}
	if got := a.AddMonths(1); got.String() != "2026-04-02" {
		t.Fatalf("AddMonths(1) = %q, want 2026-04-02", got.String())
	}
}

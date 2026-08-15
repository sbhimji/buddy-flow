package session

import (
	"testing"
	"time"
)

func ns(date, hms string) int64 {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", date+" "+hms, ET())
	if err != nil {
		panic(err)
	}
	return t.UnixNano()
}

func TestMinuteOfDay(t *testing.T) {
	cases := []struct {
		date, hms string
		want      int
	}{
		{"2026-08-14", "09:30:00", 570},
		{"2026-08-14", "09:35:59", 575},
		{"2026-08-14", "15:59:59", 959},
		{"2026-08-14", "16:00:00", 960},
		{"2026-08-14", "00:00:00", 0},
	}
	for _, c := range cases {
		if got := MinuteOfDay(ns(c.date, c.hms)); got != c.want {
			t.Errorf("MinuteOfDay(%s %s) = %d, want %d", c.date, c.hms, got, c.want)
		}
	}
}

func TestInRegular(t *testing.T) {
	for m, want := range map[int]bool{569: false, 570: true, 959: true, 960: false} {
		if InRegular(m) != want {
			t.Errorf("InRegular(%d) = %v, want %v", m, !want, want)
		}
	}
}

// TestDSTBoundary is mini-spec done-when #1: the 09:35 bucket keeps meaning
// wall-clock 09:35 ET across the 2026-11-01 DST change. A fixed-UTC-offset
// implementation passes the EDT case and fails the EST case by one hour.
func TestDSTBoundary(t *testing.T) {
	for _, date := range []string{"2026-10-30", "2026-11-02"} { // EDT side, EST side
		tsNs := ns(date, "09:35:30")
		if got := MinuteOfDay(tsNs); got != 575 {
			t.Errorf("%s: MinuteOfDay(09:35:30 ET) = %d, want 575", date, got)
		}
		if got := Date(tsNs); got != date {
			t.Errorf("Date = %s, want %s", got, date)
		}
		start, err := BucketStart(date, 575)
		if err != nil {
			t.Fatal(err)
		}
		if want := ns(date, "09:35:00") / 1e9; start != want {
			t.Errorf("%s: BucketStart(575) = %d, want %d", date, start, want)
		}
	}
	// The two 09:35s are NOT 3 days of 86400s apart — the EST one is an
	// hour later in UTC. Proves the test would catch a fixed-offset bug.
	a, _ := BucketStart("2026-10-30", 575)
	b, _ := BucketStart("2026-11-02", 575)
	if b-a != 3*86400+3600 {
		t.Errorf("EDT→EST gap = %d s, want 3d+1h; DST not exercised", b-a)
	}
}

// TestRoundTrip: Date + MinuteOfDay + BucketStart recompose to MinuteStart
// for arbitrary in-session timestamps.
func TestRoundTrip(t *testing.T) {
	for _, tsNs := range []int64{
		ns("2026-08-14", "09:30:00"),
		ns("2026-08-14", "13:00:59") + 123_456_789,
		ns("2026-11-01", "12:00:01"), // DST transition day itself
	} {
		start, err := BucketStart(Date(tsNs), MinuteOfDay(tsNs))
		if err != nil {
			t.Fatal(err)
		}
		if want := MinuteStart(tsNs); start != want {
			t.Errorf("round trip %d: BucketStart=%d MinuteStart=%d", tsNs, start, want)
		}
	}
}

func TestBucketStartValidation(t *testing.T) {
	if _, err := BucketStart("2026-08-14", 1440); err == nil {
		t.Error("minute 1440 accepted")
	}
	if _, err := BucketStart("not-a-date", 570); err == nil {
		t.Error("bad date accepted")
	}
}

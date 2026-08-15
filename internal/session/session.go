// Package session is the one tz-correct home for market-session time
// (mini-spec 2.1, PR 1). Every consumer that needs "which ET minute is this
// timestamp" or "the 09:35 bucket on date D" asks here; nobody else loads
// America/New_York or does DST math. The failure this prevents: a consumer
// that precomputes a day's origin + fixed offsets misaligns matched buckets
// by an hour across the November DST change inside a 20-day lookback —
// exactly the wrong-bucket failure the matched-baseline invariant forbids.
//
// Vocabulary: a "minute-of-day" is the ET wall-clock minute index
// (0..1439; 09:30 = 570). It is the matched-bucket key: the same
// minute-of-day on two dates is "the same time of day" regardless of DST.
// Storage keys stay absolute epoch (1.4 D1); this package is the read-time
// translation between the two.
package session

import (
	"fmt"
	"time"
)

// Regular-session bounds as minute-of-day, [Open, Close).
const (
	OpenMinute  = 9*60 + 30 // 09:30 ET = 570
	CloseMinute = 16 * 60   // 16:00 ET = 960
)

// MinutesPerSession is the number of regular-session 1-minute buckets.
const MinutesPerSession = CloseMinute - OpenMinute // 390

var et = mustLoad()

func mustLoad() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		// No tzdata means no correct bucket math anywhere; refuse to start.
		panic(fmt.Sprintf("session: load America/New_York: %v", err))
	}
	return loc
}

// ET returns the exchange time zone.
func ET() *time.Location { return et }

// MinuteOfDay returns the ET wall-clock minute (0..1439) containing the
// epoch-ns timestamp — the matched-bucket key.
func MinuteOfDay(epochNs int64) int {
	t := time.Unix(0, epochNs).In(et)
	return t.Hour()*60 + t.Minute()
}

// InRegular reports whether a minute-of-day lies in [09:30, 16:00).
func InRegular(minuteOfDay int) bool {
	return minuteOfDay >= OpenMinute && minuteOfDay < CloseMinute
}

// MinuteStart returns the epoch second of the start of the minute containing
// the epoch-ns timestamp. Purely arithmetic (minutes are 60s in UTC): this
// is the DeriveMinute key for the 1.4 bucket store.
func MinuteStart(epochNs int64) int64 {
	sec := epochNs / 1e9
	return sec - sec%60
}

// Date returns the ET calendar date ("2006-01-02") containing the epoch-ns
// timestamp — the session date for timestamps during market hours.
func Date(epochNs int64) string {
	return time.Unix(0, epochNs).In(et).Format("2006-01-02")
}

// BucketStart returns the epoch second at which the given ET minute-of-day
// begins on the given ET date ("2006-01-02") — the inverse of
// MinuteOfDay+Date, and the bridge from a matched-bucket key back to the
// absolute epoch keys the bucket store uses. DST-correct by construction:
// the wall-clock time is resolved through the location, never via a fixed
// offset from midnight.
func BucketStart(date string, minuteOfDay int) (int64, error) {
	if minuteOfDay < 0 || minuteOfDay > 1439 {
		return 0, fmt.Errorf("minute-of-day %d out of range [0,1440)", minuteOfDay)
	}
	d, err := time.ParseInLocation("2006-01-02", date, et)
	if err != nil {
		return 0, fmt.Errorf("bad date %q: %w", date, err)
	}
	t := time.Date(d.Year(), d.Month(), d.Day(), minuteOfDay/60, minuteOfDay%60, 0, 0, et)
	return t.Unix(), nil
}

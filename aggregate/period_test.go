package main

import (
	"testing"
	"time"
)

// The -year path is where published-is-the-period-end is load-bearing (the yearly
// title has no date, so a re-dispatch replaces and must not move published). now is
// deliberately far from the period end so the assertion cannot pass by both sides
// being the same value — the vacuous-test trap.
func TestAggregationPeriodYearPinsPeriodEnd(t *testing.T) {
	now := time.Date(2027, 3, 5, 10, 0, 0, 0, time.UTC)
	_, dayOut, title := aggregationPeriod(now, 14, 2026, 0)

	wantEnd := time.Date(2027, 1, 1, 0, 0, 0, 0, time.Local).Add(-time.Second)
	if !dayOut.Equal(wantEnd) {
		t.Errorf("year dayOut = %v, want end-of-2026 %v", dayOut, wantEnd)
	}
	if dayOut.Equal(now) {
		t.Error("year dayOut must be the period end, not now — equal-to-now would make this test vacuous")
	}
	if title != "Yearly - 2026" {
		t.Errorf("year title = %q, want %q", title, "Yearly - 2026")
	}
}

func TestAggregationPeriodMonthPinsPeriodEnd(t *testing.T) {
	now := time.Date(2027, 3, 5, 10, 0, 0, 0, time.UTC)
	_, dayOut, title := aggregationPeriod(now, 30, 2026, 3)

	wantEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.Local).Add(-time.Second)
	if !dayOut.Equal(wantEnd) {
		t.Errorf("month dayOut = %v, want end-of-2026-03 %v", dayOut, wantEnd)
	}
	if title != "Monthly - 2026-3" {
		t.Errorf("month title = %q, want %q", title, "Monthly - 2026-3")
	}
}

// The rolling path is correct as written and must be left alone: a rolling window
// ends now, so dayOut IS wall-clock and IS the period end. Asserting that here means
// a future change that "corrected" the rolling path would fail rather than pass.
func TestAggregationPeriodRollingEndsNow(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	dayIn, dayOut, title := aggregationPeriod(now, 14, 0, 0)

	if !dayOut.Equal(now) {
		t.Errorf("rolling dayOut = %v, want now %v", dayOut, now)
	}
	if want := now.Add(-14 * 24 * time.Hour); !dayIn.Equal(want) {
		t.Errorf("rolling dayIn = %v, want now-14d %v", dayIn, want)
	}
	if want := now.Format(time.DateOnly) + "_14-days"; title != want {
		t.Errorf("rolling title = %q, want %q", title, want)
	}
}

// The clamp to [7, 500] applies to the window but not the (pre-clamp) title,
// matching prior behaviour — deliberately not "fixed".
func TestAggregationPeriodDaysClampAppliesToWindowNotTitle(t *testing.T) {
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	dayIn, _, title := aggregationPeriod(now, 3, 0, 0) // 3 < 7 → window clamps to 7

	if want := now.Add(-7 * 24 * time.Hour); !dayIn.Equal(want) {
		t.Errorf("window should clamp to 7 days: dayIn = %v, want %v", dayIn, want)
	}
	if want := now.Format(time.DateOnly) + "_3-days"; title != want {
		t.Errorf("title should keep pre-clamp days: %q, want %q", title, want)
	}
}

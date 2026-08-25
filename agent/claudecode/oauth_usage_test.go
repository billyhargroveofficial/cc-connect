package claudecode

import (
	"testing"
	"time"
)

func floatPtr(f float64) *float64 { return &f }
func strPtr(s string) *string     { return &s }

func TestToUsageWindow_MapsUtilizationAndReset(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	period := &oauthUsagePeriod{
		Utilization: floatPtr(39.0),
		ResetsAt:    strPtr("2026-08-25T16:59:59.713223+00:00"),
	}
	w, ok := toUsageWindow("5h", fiveHourWindowSeconds, period, now)
	if !ok {
		t.Fatal("expected a window")
	}
	if w.UsedPercent != 39 {
		t.Errorf("UsedPercent = %d, want 39", w.UsedPercent)
	}
	if w.WindowSeconds != fiveHourWindowSeconds {
		t.Errorf("WindowSeconds = %d, want %d", w.WindowSeconds, fiveHourWindowSeconds)
	}
	if w.ResetAtUnix == 0 {
		t.Error("ResetAtUnix not parsed")
	}
	// 12:00 -> 16:59:59 is just under five hours.
	if w.ResetAfterSeconds < 17000 || w.ResetAfterSeconds > 18000 {
		t.Errorf("ResetAfterSeconds = %d, want ~17999", w.ResetAfterSeconds)
	}
}

func TestToUsageWindow_RoundsUtilization(t *testing.T) {
	w, _ := toUsageWindow("7d", sevenDayWindowSeconds, &oauthUsagePeriod{Utilization: floatPtr(18.6)}, time.Now())
	if w.UsedPercent != 19 {
		t.Errorf("UsedPercent = %d, want 19", w.UsedPercent)
	}
}

// An unreported quota must be omitted, not rendered as a confident zero.
func TestToUsageWindow_AbsentPeriodIsSkipped(t *testing.T) {
	if _, ok := toUsageWindow("5h", fiveHourWindowSeconds, nil, time.Now()); ok {
		t.Error("nil period should not produce a window")
	}
	if _, ok := toUsageWindow("5h", fiveHourWindowSeconds, &oauthUsagePeriod{}, time.Now()); ok {
		t.Error("period without utilization should not produce a window")
	}
}

// A past reset instant must not yield a negative countdown.
func TestToUsageWindow_StaleResetClampsCountdown(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	w, _ := toUsageWindow("5h", fiveHourWindowSeconds, &oauthUsagePeriod{
		Utilization: floatPtr(5),
		ResetsAt:    strPtr("2026-08-25T11:00:00+00:00"),
	}, now)
	if w.ResetAfterSeconds != 0 {
		t.Errorf("ResetAfterSeconds = %d, want 0 for an elapsed reset", w.ResetAfterSeconds)
	}
}

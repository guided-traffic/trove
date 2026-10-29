package jobs

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var ref = time.Date(2026, 10, 29, 12, 34, 56, 0, time.UTC) // a Thursday

func TestScheduleRejectedForms(t *testing.T) {
	// The forms that Q-STOR-4 and R-STOR-025 name.
	rejected := map[string]string{
		"six fields (seconds)":    "0 0 3 * * *",
		"seven fields (year)":     "0 0 3 * * * 2027",
		"four fields":             "0 3 * *",
		"question mark":           "0 3 ? * *",
		"quartz day of week":      "0 30 3 ? * SUN",
		"day of week 7":           "0 3 * * 7",
		"day of week list with 7": "0 3 * * 1-7",
		"L":                       "0 3 L * *",
		"nL":                      "0 3 * * 5L",
		"W":                       "0 3 15W * *",
		"hash":                    "0 3 * * 5#2",
		"descending range":        "0 3 * * 5-1",
		"descending minute range": "30-10 * * * *",
		"every":                   "@every 5m",
		"reboot":                  "@reboot",
		"unknown macro":           "@fortnightly",
		"tz prefix":               "TZ=Europe/Berlin 0 3 * * *",
		"cron tz prefix":          "CRON_TZ=UTC 0 3 * * *",
		"empty":                   "",
		"blank":                   "   ",
		"never fires (Feb 30)":    "0 0 30 2 *",
		"never fires (Apr 31)":    "0 0 31 4 *",
		"minute out of range":     "60 * * * *",
		"hour out of range":       "0 24 * * *",
		"day of month zero":       "0 0 0 * *",
		"month 13":                "0 0 1 13 *",
		"step on a single value":  "5/10 * * * *",
		"step zero":               "*/0 * * * *",
		"step beyond the range":   "*/61 * * * *",
		"empty list item":         "1,,2 * * * *",
		"name in the wrong field": "0 3 * SUN *",
		"garbage":                 "a b c d e",
		"negative":                "-1 * * * *",
		"plus sign":               "+1 * * * *",
	}
	for name, expr := range rejected {
		t.Run("R-STOR-025 rejects "+name, func(t *testing.T) {
			_, err := ParseScheduleAt(expr, ref)
			if err == nil {
				t.Fatalf("%q accepted", expr)
			}
			var se *ScheduleError
			if !errors.As(err, &se) || se.Code() != "invalid_schedule" {
				t.Fatalf("error = %v, want a ScheduleError with code invalid_schedule", err)
			}
			if !strings.Contains(err.Error(), "day of week 0-6 with Sunday = 0") {
				t.Errorf("the error does not state the dialect: %v", err)
			}
		})
	}
}

func TestScheduleAccepted(t *testing.T) {
	for _, expr := range []string{
		"* * * * *", "*/5 * * * *", "0 */6 * * *", "0 3 * * 0", "0 3 * * SUN", "0 3 * * sun", "0 3 * * MON-FRI",
		"30 3 1,15 * *", "0 0 29 2 *", "0 0 1 JAN-MAR *", "0 0 1 jan,jun *", "5-10/2 * * * *", "59 23 31 12 *",
		"@yearly", "@annually", "@monthly", "@weekly", "@daily", "@midnight", "@hourly", "@Daily", "  0 3 * * *  ", "0 3 * * 1,3,5",
	} {
		t.Run("R-STOR-025 accepts "+expr, func(t *testing.T) {
			if _, err := ParseScheduleAt(expr, ref); err != nil {
				t.Fatalf("%q rejected: %v", expr, err)
			}
		})
	}
}

func TestScheduleNextPrev(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse("2006-01-02 15:04", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		expr, from, next, prev string
	}{
		{"*/5 * * * *", "2026-10-29 12:34", "2026-10-29 12:35", "2026-10-29 12:30"},
		{"*/5 * * * *", "2026-10-29 12:35", "2026-10-29 12:40", "2026-10-29 12:35"},
		{"0 2 * * 0", "2026-10-29 12:00", "2026-11-01 02:00", "2026-10-25 02:00"},
		{"0 0 1 * *", "2026-10-29 12:00", "2026-11-01 00:00", "2026-10-01 00:00"},
		{"0 0 29 2 *", "2026-10-29 12:00", "2028-02-29 00:00", "2024-02-29 00:00"},
		{"@hourly", "2026-12-31 23:59", "2027-01-01 00:00", "2026-12-31 23:00"},
		{"0 3 * * *", "2026-10-29 03:00", "2026-10-30 03:00", "2026-10-29 03:00"},
		// POSIX day matching: both restricted, either matches (the 13th, or a Friday).
		{"0 0 13 * 5", "2026-10-29 00:00", "2026-10-30 00:00", "2026-10-23 00:00"},
		// One of them unrestricted (a leading *): both must match (odd days that are Fridays).
		{"0 0 13 * *", "2026-10-29 00:00", "2026-11-13 00:00", "2026-10-13 00:00"},
		{"0 0 */2 * 5", "2026-10-29 00:00", "2026-11-13 00:00", "2026-10-23 00:00"},
	}
	for _, c := range cases {
		t.Run("R-STOR-025 "+c.expr+" from "+c.from, func(t *testing.T) {
			s, err := ParseScheduleAt(c.expr, at(c.from))
			if err != nil {
				t.Fatal(err)
			}
			if got, ok := s.Next(at(c.from)); !ok || !got.Equal(at(c.next)) {
				t.Errorf("Next = %v %v, want %s", got, ok, c.next)
			}
			if got, ok := s.Prev(at(c.from)); !ok || !got.Equal(at(c.prev)) {
				t.Errorf("Prev = %v %v, want %s", got, ok, c.prev)
			}
		})
	}
}

func TestScheduleDefaultsFireAtTheSameSlot(t *testing.T) {
	// The default gc and gc-full schedules both fire on Sundays at 02:00 UTC
	// (R-STOR-025); the scheduler offers that one slot to both tasks.
	gc, _ := ParseScheduleAt("*/30 * * * *", ref)
	full, _ := ParseScheduleAt("0 2 * * 0", ref)
	sunday := time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)
	for name, s := range map[string]*Schedule{"gc": gc, "gc-full": full} {
		if p, ok := s.Prev(sunday); !ok || !p.Equal(sunday) {
			t.Errorf("R-STOR-025 %s: Prev(Sunday 02:00) = %v", name, p)
		}
	}
}

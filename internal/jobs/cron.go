package jobs

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CodeInvalidSchedule is the error code of a rejected schedule (R-STOR-025);
// the API answers 422 with it and the configuration loader names the key.
const CodeInvalidSchedule = "invalid_schedule"

// scheduleDialect is the sentence every rejection ends with.
const scheduleDialect = "schedules are five fields in UTC (minute hour day-of-month month day-of-week), day of week 0-6 with Sunday = 0, or one of @yearly @annually @monthly @weekly @daily @midnight @hourly"

// ScheduleError is a rejected cron expression.
type ScheduleError struct {
	Expr   string
	Reason string
}

func (e *ScheduleError) Error() string {
	return fmt.Sprintf("invalid schedule %q: %s; %s", e.Expr, e.Reason, scheduleDialect)
}

// Code is [CodeInvalidSchedule].
func (e *ScheduleError) Code() string { return CodeInvalidSchedule }

// Schedule is a parsed five-field cron expression, evaluated in UTC. It is the
// only cron parser in trove (Q-STOR-4, Q-OPS-7): maintenance tasks, retention
// policies and every later schedule use it.
type Schedule struct {
	expr                   string
	minute, hour, dom, mon uint64
	dow                    uint64
	domStar, dowStar       bool
}

// horizon is how far ahead an expression must fire to be accepted.
const horizon = 4 // years

type fieldSpec struct {
	name     string
	min, max int
	names    map[string]int
}

var (
	monthNames = map[string]int{"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6, "JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12}
	dowNames   = map[string]int{"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6}
)

func fields() [5]fieldSpec {
	return [5]fieldSpec{
		{"minute", 0, 59, nil},
		{"hour", 0, 23, nil},
		{"day of month", 1, 31, nil},
		{"month", 1, 12, monthNames},
		{"day of week", 0, 6, dowNames},
	}
}

var macros = map[string]string{
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *", "@weekly": "0 0 * * 0",
	"@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
}

// ParseSchedule parses an expression and rejects the forms Q-STOR-4 names:
// six- or seven-field expressions, ?, L, W and #, the day-of-week value 7,
// descending ranges, @every, @reboot, TZ= and CRON_TZ= prefixes, and an
// expression that does not fire within four years of now.
func ParseSchedule(expr string) (*Schedule, error) { return ParseScheduleAt(expr, time.Now()) }

// ParseScheduleAt is ParseSchedule with the reference time of the four-year
// check, for tests.
func ParseScheduleAt(expr string, now time.Time) (*Schedule, error) {
	s, err := parse(expr)
	if err != nil {
		return nil, err
	}
	if _, ok := s.Next(now); !ok {
		return nil, &ScheduleError{expr, fmt.Sprintf("it does not fire within %d years", horizon)}
	}
	return s, nil
}

func parse(expr string) (*Schedule, error) {
	bad := func(format string, a ...any) (*Schedule, error) {
		return nil, &ScheduleError{expr, fmt.Sprintf(format, a...)}
	}
	text := strings.TrimSpace(expr)
	switch {
	case text == "":
		return bad("it is empty")
	case strings.HasPrefix(text, "TZ=") || strings.HasPrefix(text, "CRON_TZ="):
		return bad("time-zone prefixes are not supported; schedules run in UTC")
	case text == "@every" || strings.HasPrefix(text, "@every ") || text == "@reboot":
		return bad("%s is not supported", strings.Fields(text)[0])
	case strings.HasPrefix(text, "@"):
		m, ok := macros[strings.ToLower(text)]
		if !ok {
			return bad("unknown macro")
		}
		text = m
	}
	parts := strings.Fields(text)
	if len(parts) != 5 {
		if len(parts) == 6 || len(parts) == 7 {
			return bad("%d fields: a seconds or year field is not supported", len(parts))
		}
		return bad("%d fields, want 5", len(parts))
	}
	s := &Schedule{expr: expr}
	specs := fields()
	var sets [5]uint64
	for i, p := range parts {
		set, err := parseField(p, specs[i])
		if err != nil {
			return bad("%s", err.Error())
		}
		sets[i] = set
	}
	s.minute, s.hour, s.dom, s.mon, s.dow = sets[0], sets[1], sets[2], sets[3], sets[4]
	s.domStar, s.dowStar = strings.HasPrefix(parts[2], "*"), strings.HasPrefix(parts[4], "*")
	return s, nil
}

func parseField(p string, f fieldSpec) (uint64, error) {
	if strings.ContainsAny(p, "?#") {
		return 0, fmt.Errorf("%s: %q uses ? or #, which are not supported", f.name, p)
	}
	up := strings.ToUpper(p)
	for _, item := range strings.Split(up, ",") {
		if item == "L" || item == "W" || strings.HasSuffix(item, "L") && isDigits(strings.TrimSuffix(item, "L")) ||
			strings.HasSuffix(item, "W") && isDigits(strings.TrimSuffix(item, "W")) {
			return 0, fmt.Errorf("%s: %q uses L or W, which are not supported", f.name, p)
		}
	}
	var set uint64
	for _, item := range strings.Split(up, ",") {
		if item == "" {
			return 0, fmt.Errorf("%s: empty list item in %q", f.name, p)
		}
		base, stepText, hasStep := strings.Cut(item, "/")
		lo, hi := f.min, f.max
		isRange := false
		switch {
		case base == "*":
			isRange = true
		case strings.Contains(base, "-"):
			a, b, _ := strings.Cut(base, "-")
			var err error
			if lo, err = value(a, f); err != nil {
				return 0, err
			}
			if hi, err = value(b, f); err != nil {
				return 0, err
			}
			if lo > hi {
				return 0, fmt.Errorf("%s: descending range %q", f.name, base)
			}
			isRange = true
		default:
			v, err := value(base, f)
			if err != nil {
				return 0, err
			}
			lo, hi = v, v
		}
		step := 1
		if hasStep {
			if !isRange {
				return 0, fmt.Errorf("%s: a step needs * or a range, not %q", f.name, base)
			}
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 || n > f.max-f.min+1 {
				return 0, fmt.Errorf("%s: step %q must be a number from 1 to %d", f.name, stepText, f.max-f.min+1)
			}
			step = n
		}
		for v := lo; v <= hi; v += step {
			set |= 1 << uint(v)
		}
	}
	return set, nil
}

func value(t string, f fieldSpec) (int, error) {
	if v, ok := f.names[t]; ok {
		return v, nil
	}
	n, err := strconv.Atoi(t)
	if err != nil || !isDigits(t) {
		return 0, fmt.Errorf("%s: %q is not a number or name", f.name, t)
	}
	if f.name == "day of week" && n == 7 {
		return 0, fmt.Errorf("day of week 7 is not accepted; Sunday is 0")
	}
	if n < f.min || n > f.max {
		return 0, fmt.Errorf("%s: %d is outside %d-%d", f.name, n, f.min, f.max)
	}
	return n, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// String is the expression as written.
func (s *Schedule) String() string { return s.expr }

func (s *Schedule) dayMatches(t time.Time) bool {
	if s.mon&(1<<uint(t.Month())) == 0 {
		return false
	}
	dom := s.dom&(1<<uint(t.Day())) != 0
	dow := s.dow&(1<<uint(t.Weekday())) != 0
	if !s.domStar && !s.dowStar {
		return dom || dow // POSIX: both restricted, either matches
	}
	return dom && dow
}

// Next returns the first fire time strictly after t, in UTC, or false if there
// is none within four years.
func (s *Schedule) Next(t time.Time) (time.Time, bool) {
	t = t.UTC().Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(horizon, 0, 1)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	for ; day.Before(limit); day = day.AddDate(0, 0, 1) {
		if !s.dayMatches(day) {
			continue
		}
		h0, m0 := 0, 0
		if day.Equal(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)) {
			h0, m0 = t.Hour(), t.Minute()
		}
		for h := h0; h < 24; h++ {
			if s.hour&(1<<uint(h)) == 0 {
				continue
			}
			from := 0
			if h == h0 {
				from = m0
			}
			for m := from; m < 60; m++ {
				if s.minute&(1<<uint(m)) != 0 {
					return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute), true
				}
			}
		}
	}
	return time.Time{}, false
}

// Prev returns the latest fire time at or before t, in UTC, or false if there is
// none within four years back. The scheduler uses it to find a task's latest due
// slot (R-ARCH-016).
func (s *Schedule) Prev(t time.Time) (time.Time, bool) {
	t = t.UTC().Truncate(time.Minute)
	limit := t.AddDate(-horizon, 0, -1)
	startDay := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	for day := startDay; day.After(limit); day = day.AddDate(0, 0, -1) {
		if !s.dayMatches(day) {
			continue
		}
		h1, m1 := 23, 59
		if day.Equal(startDay) {
			h1, m1 = t.Hour(), t.Minute()
		}
		for h := h1; h >= 0; h-- {
			if s.hour&(1<<uint(h)) == 0 {
				continue
			}
			from := 59
			if h == h1 {
				from = m1
			}
			for m := from; m >= 0; m-- {
				if s.minute&(1<<uint(m)) != 0 {
					return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute), true
				}
			}
		}
	}
	return time.Time{}, false
}

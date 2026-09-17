package assistant

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Window text and timezones for the schedule tools.
//
// STRICT, BECAUSE THE ROUTE CANNOT CATCH WHAT THIS GETS WRONG. A window the
// parser misreads -- one day instead of five, an overnight span instead of
// a working day -- is a valid window to the route, is written, and admits
// or refuses people on a schedule nobody asked for; the confirmation card
// shows the text, not the parse. So every token must be one of a small set
// of exact spellings, and anything else is refused with the form to use.

// Mon=1 .. Sun=64, the mask migration 002 established.
var dayBits = map[string]int{
	"mon": 1, "tue": 2, "wed": 4, "thu": 8, "fri": 16, "sat": 32, "sun": 64,
}

var dayOrder = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// dayNames are the only accepted spellings: three letters or the full name.
var dayNames = map[string]int{
	"mon": 0, "monday": 0, "tue": 1, "tuesday": 1, "wed": 2, "wednesday": 2, "thu": 3, "thursday": 3,
	"fri": 4, "friday": 4, "sat": 5, "saturday": 5, "sun": 6, "sunday": 6,
}

const (
	everyDay = 127
	weekdays = 1 | 2 | 4 | 8 | 16
	weekends = 32 | 64
)

const dayFormHint = `days are "Daily", "Weekdays", "Weekends", day names (Mon or Monday) separated by commas, or a range with a hyphen (Mon-Fri)`

// clockPattern is the whole of an accepted time: HH:MM, 24-hour, one or two
// hour digits, and nothing else -- no am/pm, no seconds, no trailing text.
var clockPattern = regexp.MustCompile(`^([01]?[0-9]|2[0-4]):([0-5][0-9])$`)

// parseWindows turns "Mon-Fri 08:00-18:00; Sat 09:00-13:00" into the route's
// windows. Each entry is DAYS then TIMES, separated by spaces; entries are
// separated by ";".
func parseWindows(text string) ([]object, error) {
	out := []object{}
	for _, entry := range strings.Split(text, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		fields := strings.Fields(entry)
		if len(fields) < 2 {
			return nil, fmt.Errorf("window %q: expected days then a time range, e.g. \"Mon-Fri 08:00-18:00\"", entry)
		}
		times := fields[len(fields)-1]
		days := strings.Join(fields[:len(fields)-1], " ")
		mask, err := parseDays(days)
		if err != nil {
			return nil, fmt.Errorf("window %q: %v", entry, err)
		}
		start, end, err := parseTimeRange(times)
		if err != nil {
			return nil, fmt.Errorf("window %q: %v", entry, err)
		}
		out = append(out, object{"days_of_week": mask, "start_time": start, "end_time": end})
	}
	if len(out) == 0 {
		return nil, errText("windows is empty; give at least one, e.g. \"Mon-Fri 08:00-18:00\"")
	}
	return out, nil
}

// parseDays reads the day part of a window. Only exact spellings are
// accepted; a separator that is not "," or "-" (the word "to" or "and", "&",
// an en or em dash, a slash) is refused rather than guessed at.
func parseDays(s string) (int, error) {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "daily", "everyday", "every day", "all":
		return everyDay, nil
	case "weekdays":
		return weekdays, nil
	case "weekends", "weekend":
		return weekends, nil
	}
	padded := " " + strings.ToLower(s) + " "
	if strings.ContainsAny(s, "–—&/+") || strings.Contains(padded, " to ") || strings.Contains(padded, " and ") {
		return 0, fmt.Errorf("days %q: write a range as Mon-Fri and a list as Sat,Sun; %s", s, dayFormHint)
	}
	mask := 0
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return 0, fmt.Errorf("days %q: empty entry between commas; %s", s, dayFormHint)
		}
		if strings.Contains(part, "-") {
			from, to, _ := strings.Cut(part, "-")
			if strings.Contains(to, "-") {
				return 0, fmt.Errorf("days %q: a range has one hyphen, e.g. Mon-Fri", part)
			}
			a, err := dayIndex(from)
			if err != nil {
				return 0, err
			}
			b, err := dayIndex(to)
			if err != nil {
				return 0, err
			}
			for i := a; ; i = (i + 1) % 7 {
				mask |= dayBits[dayOrder[i]]
				if i == b {
					break
				}
			}
			continue
		}
		i, err := dayIndex(part)
		if err != nil {
			return 0, err
		}
		mask |= dayBits[dayOrder[i]]
	}
	if mask == 0 {
		return 0, errText("no days given; " + dayFormHint)
	}
	return mask, nil
}

// dayIndex accepts exactly a three-letter or full day name, nothing else.
func dayIndex(name string) (int, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if i, ok := dayNames[key]; ok {
		return i, nil
	}
	return 0, fmt.Errorf("unknown day %q; %s", strings.TrimSpace(name), dayFormHint)
}

// parseTimeRange reads HH:MM-HH:MM. A start of 24:00 is refused (it is the
// next day's 00:00); an end of 24:00 means "to the end of the day" and is
// written as 23:59, the last minute the route can express -- never as 00:00,
// which the route would read as a zero-length or overnight window.
func parseTimeRange(s string) (string, string, error) {
	from, to, ok := strings.Cut(s, "-")
	if !ok || strings.Contains(to, "-") {
		return "", "", fmt.Errorf("time range %q must be HH:MM-HH:MM (24-hour)", s)
	}
	start, err := normaliseClock(from)
	if err != nil {
		return "", "", err
	}
	if start == "24:00" {
		return "", "", errText("a window cannot start at 24:00; use 00:00 for the start of the day")
	}
	end, err := normaliseClock(to)
	if err != nil {
		return "", "", err
	}
	if end == "24:00" {
		end = "23:59"
	}
	return start, end, nil
}

// normaliseClock accepts the whole of an HH:MM 24-hour time and returns it
// zero-padded. 24:00 is returned as-is for parseTimeRange to decide.
func normaliseClock(s string) (string, error) {
	s = strings.TrimSpace(s)
	m := clockPattern.FindStringSubmatch(s)
	if m == nil {
		return "", fmt.Errorf("time %q must be HH:MM in 24-hour form, e.g. 09:00 or 17:30 (not 5pm)", s)
	}
	h, _ := strconv.Atoi(m[1])
	if h == 24 && m[2] != "00" {
		return "", fmt.Errorf("time %q is past the end of the day", s)
	}
	return fmt.Sprintf("%02d:%s", h, m[2]), nil
}

// validTimezone refuses a zone the platform could not evaluate. The route
// stores any string, and the evaluator then refuses every rule using the
// schedule (an ALLOW that cannot be evaluated does not allow) -- so a typo
// here would lock people out silently. "Local" names the server's zone,
// which is never what an operator means.
func validTimezone(tz string) error {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return nil
	}
	if strings.EqualFold(tz, "Local") {
		return errText("timezone must be an IANA zone name such as Africa/Lagos or Europe/London")
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return fmt.Errorf("timezone %q is not a known IANA zone; use a name such as Africa/Lagos or Europe/London", tz)
	}
	return nil
}

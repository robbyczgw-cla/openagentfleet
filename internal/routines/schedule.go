package routines

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ScheduleKind is the deliberately small set of schedules supported by the
// local routine core. It is not a cron expression and must not be expanded
// into arbitrary cron execution by callers.
type ScheduleKind string

const (
	ScheduleHourly ScheduleKind = "hourly"
	ScheduleDaily  ScheduleKind = "daily"
	ScheduleWeekly ScheduleKind = "weekly"
)

var ErrInvalidSchedule = errors.New("invalid routine schedule")

// Schedule is a validated recurring schedule.
//
// Supported syntax is intentionally bounded:
//   - hourly
//   - hourly :15
//   - daily 09:30
//   - weekly mon 09:30
//
// The optional word "at" is accepted for readability (for example,
// "daily at 09:30"). No cron syntax, seconds, dates, time zones, or monthly
// schedules are accepted here.
type Schedule struct {
	Kind    ScheduleKind `json:"kind"`
	Minute  int          `json:"minute"`
	Hour    int          `json:"hour"`
	Weekday time.Weekday `json:"weekday"`
}

func NewHourly(minute int) (Schedule, error) {
	schedule := Schedule{Kind: ScheduleHourly, Minute: minute}
	return schedule, schedule.Validate()
}

func NewDaily(hour, minute int) (Schedule, error) {
	schedule := Schedule{Kind: ScheduleDaily, Hour: hour, Minute: minute}
	return schedule, schedule.Validate()
}

func NewWeekly(weekday time.Weekday, hour, minute int) (Schedule, error) {
	schedule := Schedule{Kind: ScheduleWeekly, Weekday: weekday, Hour: hour, Minute: minute}
	return schedule, schedule.Validate()
}

// ParseSchedule validates and parses the bounded human-readable schedule
// grammar. Callers should persist Schedule.String() rather than treating this
// as a general-purpose scheduler expression.
func ParseSchedule(expression string) (Schedule, error) {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(expression)))
	if len(fields) == 0 {
		return Schedule{}, fmt.Errorf("%w: expression is empty", ErrInvalidSchedule)
	}

	switch fields[0] {
	case string(ScheduleHourly):
		minute := 0
		switch len(fields) {
		case 1:
			// The top of the hour is the default hourly schedule.
		case 2:
			var err error
			minute, err = parseMinute(fields[1])
			if err != nil {
				return Schedule{}, err
			}
		case 3:
			if fields[1] != "at" {
				return Schedule{}, fmt.Errorf("%w: expected 'at' before hourly minute", ErrInvalidSchedule)
			}
			var err error
			minute, err = parseMinute(fields[2])
			if err != nil {
				return Schedule{}, err
			}
		default:
			return Schedule{}, fmt.Errorf("%w: hourly accepts only an optional minute", ErrInvalidSchedule)
		}
		return NewHourly(minute)

	case string(ScheduleDaily):
		clockText, err := parseAtValue(fields, 1)
		if err != nil {
			return Schedule{}, err
		}
		hour, minute, err := parseClock(clockText)
		if err != nil {
			return Schedule{}, err
		}
		return NewDaily(hour, minute)

	case string(ScheduleWeekly):
		if len(fields) < 3 {
			return Schedule{}, fmt.Errorf("%w: weekly requires a weekday and time", ErrInvalidSchedule)
		}
		weekday, ok := parseWeekday(fields[1])
		if !ok {
			return Schedule{}, fmt.Errorf("%w: unknown weekday %q", ErrInvalidSchedule, fields[1])
		}
		clockText, err := parseAtValue(fields, 2)
		if err != nil {
			return Schedule{}, err
		}
		hour, minute, err := parseClock(clockText)
		if err != nil {
			return Schedule{}, err
		}
		return NewWeekly(weekday, hour, minute)

	default:
		return Schedule{}, fmt.Errorf("%w: only hourly, daily, and weekly are supported", ErrInvalidSchedule)
	}
}

func parseAtValue(fields []string, index int) (string, error) {
	if len(fields) == index+1 {
		return fields[index], nil
	}
	if len(fields) == index+2 && fields[index] == "at" {
		return fields[index+1], nil
	}
	return "", fmt.Errorf("%w: expected one time value", ErrInvalidSchedule)
}

func parseMinute(value string) (int, error) {
	if len(value) != 3 || value[0] != ':' {
		return 0, fmt.Errorf("%w: hourly minute must look like :15", ErrInvalidSchedule)
	}
	minute, err := parseTwoDigits(value[1:])
	if err != nil || minute > 59 {
		return 0, fmt.Errorf("%w: hourly minute must be between :00 and :59", ErrInvalidSchedule)
	}
	return minute, nil
}

func parseClock(value string) (int, int, error) {
	if len(value) != 5 || value[2] != ':' {
		return 0, 0, fmt.Errorf("%w: time must look like 09:30", ErrInvalidSchedule)
	}
	hour, hourErr := parseTwoDigits(value[:2])
	minute, minuteErr := parseTwoDigits(value[3:])
	if hourErr != nil || minuteErr != nil || hour > 23 || minute > 59 {
		return 0, 0, fmt.Errorf("%w: time must be between 00:00 and 23:59", ErrInvalidSchedule)
	}
	return hour, minute, nil
}

func parseTwoDigits(value string) (int, error) {
	if len(value) != 2 || value[0] < '0' || value[0] > '9' || value[1] < '0' || value[1] > '9' {
		return 0, errors.New("not two digits")
	}
	return strconv.Atoi(value)
}

func parseWeekday(value string) (time.Weekday, bool) {
	weekdays := map[string]time.Weekday{
		"sun": time.Sunday, "sunday": time.Sunday,
		"mon": time.Monday, "monday": time.Monday,
		"tue": time.Tuesday, "tues": time.Tuesday, "tuesday": time.Tuesday,
		"wed": time.Wednesday, "wednesday": time.Wednesday,
		"thu": time.Thursday, "thur": time.Thursday, "thurs": time.Thursday, "thursday": time.Thursday,
		"fri": time.Friday, "friday": time.Friday,
		"sat": time.Saturday, "saturday": time.Saturday,
	}
	weekday, ok := weekdays[value]
	return weekday, ok
}

// Validate checks the structured representation as well as values produced by
// ParseSchedule. This is useful at persistence/API boundaries.
func (s Schedule) Validate() error {
	if s.Minute < 0 || s.Minute > 59 {
		return fmt.Errorf("%w: minute must be between 0 and 59", ErrInvalidSchedule)
	}
	switch s.Kind {
	case ScheduleHourly:
		if s.Hour != 0 || s.Weekday != time.Sunday {
			return fmt.Errorf("%w: hourly cannot contain a day or hour", ErrInvalidSchedule)
		}
	case ScheduleDaily:
		if s.Hour < 0 || s.Hour > 23 || s.Weekday != time.Sunday {
			return fmt.Errorf("%w: daily requires only a valid hour and minute", ErrInvalidSchedule)
		}
	case ScheduleWeekly:
		if s.Hour < 0 || s.Hour > 23 || s.Weekday < time.Sunday || s.Weekday > time.Saturday {
			return fmt.Errorf("%w: weekly requires a valid weekday, hour, and minute", ErrInvalidSchedule)
		}
	default:
		return fmt.Errorf("%w: unsupported schedule kind %q", ErrInvalidSchedule, s.Kind)
	}
	return nil
}

// NextAfter returns the first occurrence strictly after reference. Calendar
// arithmetic is used for daily and weekly schedules so daylight-saving changes
// do not turn a wall-clock schedule into a fixed 24-hour interval.
func (s Schedule) NextAfter(reference time.Time) (time.Time, error) {
	if err := s.Validate(); err != nil {
		return time.Time{}, err
	}
	location := reference.Location()
	if location == nil {
		location = time.UTC
	}

	switch s.Kind {
	case ScheduleHourly:
		candidate := time.Date(reference.Year(), reference.Month(), reference.Day(), reference.Hour(), s.Minute, 0, 0, location)
		if !candidate.After(reference) {
			candidate = candidate.Add(time.Hour)
		}
		return candidate, nil

	case ScheduleDaily:
		candidate := time.Date(reference.Year(), reference.Month(), reference.Day(), s.Hour, s.Minute, 0, 0, location)
		if !candidate.After(reference) {
			candidate = candidate.AddDate(0, 0, 1)
		}
		return candidate, nil

	case ScheduleWeekly:
		dayDelta := (int(s.Weekday) - int(reference.Weekday()) + 7) % 7
		candidate := time.Date(reference.Year(), reference.Month(), reference.Day(), s.Hour, s.Minute, 0, 0, location)
		candidate = candidate.AddDate(0, 0, dayDelta)
		if !candidate.After(reference) {
			candidate = candidate.AddDate(0, 0, 7)
		}
		return candidate, nil

	default:
		return time.Time{}, fmt.Errorf("%w: unsupported schedule kind %q", ErrInvalidSchedule, s.Kind)
	}
}

// NextRun calculates the next occurrence using the caller-supplied clock.
func (s Schedule) NextRun(clock Clock) (time.Time, error) {
	if clock == nil {
		return time.Time{}, errors.New("routine clock is nil")
	}
	return s.NextAfter(clock.Now())
}

func (s Schedule) String() string {
	if err := s.Validate(); err != nil {
		return "invalid"
	}
	switch s.Kind {
	case ScheduleHourly:
		if s.Minute == 0 {
			return "hourly"
		}
		return fmt.Sprintf("hourly :%02d", s.Minute)
	case ScheduleDaily:
		return fmt.Sprintf("daily %02d:%02d", s.Hour, s.Minute)
	case ScheduleWeekly:
		return fmt.Sprintf("weekly %s %02d:%02d", weekdayName(s.Weekday), s.Hour, s.Minute)
	default:
		return "invalid"
	}
}

func weekdayName(weekday time.Weekday) string {
	return [...]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}[weekday]
}

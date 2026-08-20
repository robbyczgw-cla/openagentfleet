package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// NextCronTime returns the first minute strictly after `after` that matches
// the five-field cron expression in the named time zone. Seconds are ignored.
func NextCronTime(expression, timeZone string, after time.Time) (time.Time, error) {
	expression = strings.Join(strings.Fields(expression), " ")
	if !validStoredCronExpression(expression) {
		return time.Time{}, errors.New("routine cron expression must contain five valid numeric fields")
	}
	location, err := time.LoadLocation(strings.TrimSpace(timeZone))
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid routine time zone %q: %w", timeZone, err)
	}
	if after.IsZero() {
		after = time.Now()
	}
	cursor := after.In(location).Truncate(time.Minute).Add(time.Minute)
	fields := strings.Fields(expression)
	const maxMinutes = 366 * 24 * 60
	for index := 0; index < maxMinutes; index++ {
		if cronTimeMatches(fields, cursor) {
			return cursor.UTC(), nil
		}
		cursor = cursor.Add(time.Minute)
	}
	return time.Time{}, errors.New("no matching cron time in the next year")
}

// DefaultNextRunAt is the first wake after `after` for an enabled routine.
// Cron uses the expression; heartbeat uses the configured interval.
func DefaultNextRunAt(routine Routine, after time.Time) (time.Time, error) {
	if after.IsZero() {
		after = time.Now()
	}
	switch routine.Kind {
	case RoutineKindHeartbeat:
		if routine.HeartbeatIntervalSeconds < RoutineHeartbeatMinSeconds {
			return time.Time{}, fmt.Errorf("heartbeat interval must be between %d and %d seconds", RoutineHeartbeatMinSeconds, RoutineHeartbeatMaxSeconds)
		}
		return after.UTC().Add(time.Duration(routine.HeartbeatIntervalSeconds) * time.Second), nil
	case RoutineKindCron:
		return NextCronTime(routine.CronExpression, routine.TimeZone, after)
	default:
		return time.Time{}, fmt.Errorf("unsupported routine kind %q", routine.Kind)
	}
}

func cronTimeMatches(fields []string, value time.Time) bool {
	if !cronFieldMatches(fields[0], value.Minute(), 0, 59) {
		return false
	}
	if !cronFieldMatches(fields[1], value.Hour(), 0, 23) {
		return false
	}
	if !cronFieldMatches(fields[3], int(value.Month()), 1, 12) {
		return false
	}
	dayOfMonth := cronFieldMatches(fields[2], value.Day(), 1, 31)
	dayOfWeek := cronWeekdayMatches(fields[4], value.Weekday())
	domRestricted := fields[2] != "*"
	dowRestricted := fields[4] != "*"
	if domRestricted && dowRestricted {
		return dayOfMonth || dayOfWeek
	}
	return dayOfMonth && dayOfWeek
}

func cronWeekdayMatches(field string, weekday time.Weekday) bool {
	value := int(weekday)
	if cronFieldMatches(field, value, 0, 7) {
		return true
	}
	return value == 0 && cronFieldMatches(field, 7, 0, 7)
}

func cronFieldMatches(field string, value, minimum, maximum int) bool {
	for _, listItem := range strings.Split(field, ",") {
		if listItem == "" {
			return false
		}
		base := listItem
		step := 1
		if strings.Contains(listItem, "/") {
			parts := strings.Split(listItem, "/")
			if len(parts) != 2 {
				return false
			}
			parsedStep, err := strconv.Atoi(parts[1])
			if err != nil || parsedStep < 1 {
				return false
			}
			step = parsedStep
			base = parts[0]
		}
		start, end := minimum, maximum
		if base != "*" {
			if strings.Contains(base, "-") {
				bounds := strings.Split(base, "-")
				if len(bounds) != 2 {
					return false
				}
				parsedStart, startErr := strconv.Atoi(bounds[0])
				parsedEnd, endErr := strconv.Atoi(bounds[1])
				if startErr != nil || endErr != nil {
					return false
				}
				start, end = parsedStart, parsedEnd
			} else {
				parsed, err := strconv.Atoi(base)
				if err != nil {
					return false
				}
				start, end = parsed, parsed
			}
		}
		if value < start || value > end {
			continue
		}
		if (value-start)%step == 0 {
			return true
		}
	}
	return false
}

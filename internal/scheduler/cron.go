package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ScheduleExpr represents a parsed 5-field Cron schedule expression.
type ScheduleExpr struct {
	raw     string
	minutes map[int]bool
	hours   map[int]bool
	doms    map[int]bool
	months  map[int]bool
	dows    map[int]bool
}

// ParseCron parses standard 5-part cron syntax or macros.
func ParseCron(expr string) (*ScheduleExpr, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, fmt.Errorf("cron expression cannot be empty")
	}

	// Expand standard macros
	switch strings.ToLower(expr) {
	case "@yearly", "@annually":
		expr = "0 0 1 1 *"
	case "@monthly":
		expr = "0 0 1 * *"
	case "@weekly":
		expr = "0 0 * * 0"
	case "@daily", "@midnight":
		expr = "0 0 * * *"
	case "@hourly":
		expr = "0 * * * *"
	}

	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron expression must have 5 fields (minute hour dom month dow), got %d fields", len(fields))
	}

	minutes, err := parseCronField(fields[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("invalid minute field '%s': %w", fields[0], err)
	}

	hours, err := parseCronField(fields[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("invalid hour field '%s': %w", fields[1], err)
	}

	doms, err := parseCronField(fields[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("invalid day-of-month field '%s': %w", fields[2], err)
	}

	months, err := parseCronField(fields[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("invalid month field '%s': %w", fields[3], err)
	}

	dows, err := parseCronField(fields[4], 0, 6)
	if err != nil {
		return nil, fmt.Errorf("invalid day-of-week field '%s': %w", fields[4], err)
	}

	return &ScheduleExpr{
		raw:     expr,
		minutes: minutes,
		hours:   hours,
		doms:    doms,
		months:  months,
		dows:    dows,
	}, nil
}

// Next calculates the earliest time after `from` that matches the cron schedule.
func (s *ScheduleExpr) Next(from time.Time) time.Time {
	// Truncate to the nearest minute, advance 1 minute
	t := from.Truncate(time.Minute).Add(time.Minute)

	// Search up to 5 years into the future
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		month := int(t.Month())
		if !s.months[month] {
			// Advance to next month
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}

		dom := t.Day()
		dow := int(t.Weekday())
		if !s.doms[dom] || !s.dows[dow] {
			// Advance to next day
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}

		hour := t.Hour()
		if !s.hours[hour] {
			// Advance to next hour
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}

		minute := t.Minute()
		if !s.minutes[minute] {
			t = t.Add(time.Minute)
			continue
		}

		return t
	}

	return time.Time{}
}

// IsValidCron returns true if the expression can be parsed successfully.
func IsValidCron(expr string) bool {
	_, err := ParseCron(expr)
	return err == nil
}

// HumanDescription converts common cron expressions into natural language descriptions.
func HumanDescription(expr string) string {
	expr = strings.TrimSpace(expr)
	switch strings.ToLower(expr) {
	case "@yearly", "@annually", "0 0 1 1 *":
		return "Annually on January 1 at 00:00"
	case "@monthly", "0 0 1 * *":
		return "Monthly on the 1st day at 00:00"
	case "@weekly", "0 0 * * 0":
		return "Weekly on Sunday at 00:00"
	case "@daily", "@midnight", "0 0 * * *":
		return "Daily at midnight (00:00 UTC)"
	case "@hourly", "0 * * * *":
		return "Hourly at minute 0"
	case "*/5 * * * *":
		return "Every 5 minutes"
	case "*/10 * * * *":
		return "Every 10 minutes"
	case "*/15 * * * *":
		return "Every 15 minutes"
	case "*/30 * * * *":
		return "Every 30 minutes"
	case "0 2 * * *":
		return "Daily at 02:00 AM"
	case "0 12 * * *":
		return "Daily at 12:00 PM (Noon)"
	case "0 0 * * 1":
		return "Weekly on Monday at 00:00"
	case "0 0 * * 1-5":
		return "Weekdays (Mon-Fri) at 00:00"
	}

	fields := strings.Fields(expr)
	if len(fields) == 5 {
		if strings.HasPrefix(fields[0], "*/") && fields[1] == "*" && fields[2] == "*" && fields[3] == "*" && fields[4] == "*" {
			step := strings.TrimPrefix(fields[0], "*/")
			return fmt.Sprintf("Every %s minutes", step)
		}
		if fields[2] == "*" && fields[3] == "*" && fields[4] == "*" {
			return fmt.Sprintf("Daily at %02s:%02s", fields[1], fields[0])
		}
	}
	return fmt.Sprintf("Cron: %s", expr)
}

func parseCronField(field string, min, max int) (map[int]bool, error) {
	result := make(map[int]bool)
	parts := strings.Split(field, ",")

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		if part == "*" {
			for i := min; i <= max; i++ {
				result[i] = true
			}
			continue
		}

		if strings.HasPrefix(part, "*/") {
			stepStr := strings.TrimPrefix(part, "*/")
			step, err := strconv.Atoi(stepStr)
			if err != nil || step <= 0 {
				return nil, fmt.Errorf("invalid step '%s'", stepStr)
			}
			for i := min; i <= max; i += step {
				result[i] = true
			}
			continue
		}

		if strings.Contains(part, "-") {
			rangeParts := strings.Split(part, "/")
			subRange := strings.Split(rangeParts[0], "-")
			if len(subRange) != 2 {
				return nil, fmt.Errorf("invalid range syntax '%s'", part)
			}
			start, err1 := strconv.Atoi(subRange[0])
			end, err2 := strconv.Atoi(subRange[1])
			if err1 != nil || err2 != nil || start < min || end > max || start > end {
				return nil, fmt.Errorf("invalid range '%s'", rangeParts[0])
			}
			step := 1
			if len(rangeParts) == 2 {
				step, err1 = strconv.Atoi(rangeParts[1])
				if err1 != nil || step <= 0 {
					return nil, fmt.Errorf("invalid step in range '%s'", part)
				}
			}
			for i := start; i <= end; i += step {
				result[i] = true
			}
			continue
		}

		val, err := strconv.Atoi(part)
		if err != nil || val < min || val > max {
			return nil, fmt.Errorf("value %s out of range (%d-%d)", part, min, max)
		}
		result[val] = true
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("empty field specification")
	}

	return result, nil
}

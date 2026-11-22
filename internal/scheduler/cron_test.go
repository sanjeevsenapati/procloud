package scheduler

import (
	"testing"
	"time"
)

func TestParseCronAndNext(t *testing.T) {
	tests := []struct {
		name       string
		expr       string
		from       time.Time
		expected   time.Time
		valid      bool
		descSubstr string
	}{
		{
			name:       "Daily at 2am",
			expr:       "0 2 * * *",
			from:       time.Date(2026, 11, 22, 1, 30, 0, 0, time.UTC),
			expected:   time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC),
			valid:      true,
			descSubstr: "02:00",
		},
		{
			name:       "Every 15 minutes",
			expr:       "*/15 * * * *",
			from:       time.Date(2026, 11, 22, 10, 14, 0, 0, time.UTC),
			expected:   time.Date(2026, 11, 22, 10, 15, 0, 0, time.UTC),
			valid:      true,
			descSubstr: "15 minutes",
		},
		{
			name:       "Macro @hourly",
			expr:       "@hourly",
			from:       time.Date(2026, 11, 22, 14, 25, 0, 0, time.UTC),
			expected:   time.Date(2026, 11, 22, 15, 0, 0, 0, time.UTC),
			valid:      true,
			descSubstr: "Hourly",
		},
		{
			name:       "Macro @daily",
			expr:       "@daily",
			from:       time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC),
			expected:   time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC),
			valid:      true,
			descSubstr: "Daily",
		},
		{
			name:       "Invalid syntax - too few fields",
			expr:       "* * *",
			valid:      false,
		},
		{
			name:       "Invalid minute - out of bounds",
			expr:       "65 * * * *",
			valid:      false,
		},
		{
			name:       "Invalid hour - out of bounds",
			expr:       "0 25 * * *",
			valid:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := ParseCron(tt.expr)
			if tt.valid {
				if err != nil {
					t.Fatalf("expected valid cron, got err: %v", err)
				}
				if !IsValidCron(tt.expr) {
					t.Fatalf("IsValidCron returned false for valid expression %s", tt.expr)
				}
				next := parsed.Next(tt.from)
				if !next.Equal(tt.expected) {
					t.Errorf("Next(): expected %v, got %v", tt.expected, next)
				}
				desc := HumanDescription(tt.expr)
				if tt.descSubstr != "" && !containsIgnoreCase(desc, tt.descSubstr) {
					t.Errorf("HumanDescription(): expected to contain '%s', got '%s'", tt.descSubstr, desc)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error for invalid cron %s, got nil", tt.expr)
				}
				if IsValidCron(tt.expr) {
					t.Fatalf("IsValidCron returned true for invalid expression %s", tt.expr)
				}
			}
		})
	}
}

func containsIgnoreCase(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		len(s) > 0 && len(substr) > 0 && (contains(s, substr)))
}

func contains(s, substr string) bool {
	return len(substr) <= len(s) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && (s[:len(substr)] == substr || contains(s[1:], substr))))
}

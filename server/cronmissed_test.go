package main

import (
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/cron"
	"github.com/pocketbase/pocketbase/tools/types"
)

func TestCountMissedRuns(t *testing.T) {
	base := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name  string
		expr  string
		since time.Time
		now   time.Time
		want  int
	}{
		{"nothing due over the interval", "0 0 * * *", base.Add(10 * time.Hour), base.Add(12 * time.Hour), 0},
		{"same minute", "* * * * *", base, base, 0},
		{"every minute over five minutes", "* * * * *", base.Add(10 * time.Hour), base.Add(10*time.Hour + 5*time.Minute), 4},
		{"hourly over six hours", "0 * * * *", base, base.Add(6 * time.Hour), 5},
		{"daily over three days", "0 0 * * *", base, base.Add(72 * time.Hour), 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schedule, err := cron.NewSchedule(tc.expr)
			if err != nil {
				t.Fatalf("invalid test expression %q: %v", tc.expr, err)
			}

			got, capped := countMissedRuns(schedule, tc.since, tc.now)
			if got != tc.want {
				t.Errorf("countMissedRuns() = %d, want %d", got, tc.want)
			}
			if capped {
				t.Error("countMissedRuns() reported a capped walk on a short interval")
			}
		})
	}
}

func TestCountMissedRuns_CappedAtLookback(t *testing.T) {
	schedule, err := cron.NewSchedule("* * * * *")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	since := now.Add(-90 * 24 * time.Hour)

	got, capped := countMissedRuns(schedule, since, now)
	if !capped {
		t.Error("countMissedRuns() should report a capped walk on a 90-day interval")
	}

	// A minutely schedule is due on every walked minute: the count doubles as
	// the iteration count, which must stop at the lookback bound.
	want := int(maxMissedLookback / time.Minute)
	if got != want {
		t.Errorf("countMissedRuns() walked %d minutes, want %d (capped at %s)", got, want, maxMissedLookback)
	}
}

// missedLogs returns the faasbox_logs entries carrying the "missed" status.
func missedLogs(t testing.TB, app core.App) []*core.Record {
	t.Helper()
	records, err := app.FindAllRecords(faasboxLogsCollection)
	if err != nil {
		t.Fatalf("failed to read logs: %v", err)
	}

	var missed []*core.Record
	for _, record := range records {
		if record.GetString("status") == "missed" {
			missed = append(missed, record)
		}
	}
	return missed
}

func TestReportMissedCronRuns(t *testing.T) {
	now := time.Now()

	t.Run("one entry per job whatever the number of occurrences", func(t *testing.T) {
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()
		setupFaaSCollections(t, app)
		fn := saveTestFunction(t, app, t.TempDir(), "echo", "console.log(1)", "")

		record := createTestCronJob(t, app, "missed-cron", "* * * * *", fn.Id, true)
		setCronJobDate(t, app, record.Id, "lastRunAt", now.Add(-10*time.Minute))

		reportMissedCronRuns(app, now)

		entries := missedLogs(t, app)
		if len(entries) != 1 {
			t.Fatalf("expected exactly 1 missed entry, got %d", len(entries))
		}
		if got := entries[0].GetString("trigger"); got != "cron" {
			t.Errorf("trigger = %q, want %q", got, "cron")
		}
		if got := decryptedText(app, entries[0], "functionName"); got != "echo" {
			t.Errorf("functionName = %q, want %q", got, "echo")
		}
		if decryptedText(app, entries[0], "stderr") == "" {
			t.Error("missed entry carries no description of the occurrences and period")
		}
	})

	t.Run("inactive job is never reported", func(t *testing.T) {
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()
		setupFaaSCollections(t, app)
		fn := saveTestFunction(t, app, t.TempDir(), "echo", "console.log(1)", "")

		record := createTestCronJob(t, app, "inactive-cron", "* * * * *", fn.Id, false)
		setCronJobDate(t, app, record.Id, "lastRunAt", now.Add(-10*time.Minute))

		reportMissedCronRuns(app, now)

		if entries := missedLogs(t, app); len(entries) != 0 {
			t.Errorf("inactive job produced %d missed entries", len(entries))
		}
	})

	t.Run("job that never ran falls back to created", func(t *testing.T) {
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()
		setupFaaSCollections(t, app)
		fn := saveTestFunction(t, app, t.TempDir(), "echo", "console.log(1)", "")

		record := createTestCronJob(t, app, "never-ran", "* * * * *", fn.Id, true)
		setCronJobDate(t, app, record.Id, "created", now.Add(-10*time.Minute))

		reportMissedCronRuns(app, now)

		if entries := missedLogs(t, app); len(entries) != 1 {
			t.Fatalf("expected exactly 1 missed entry from the created fallback, got %d", len(entries))
		}
	})

	t.Run("job created just now reports nothing", func(t *testing.T) {
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()
		setupFaaSCollections(t, app)
		fn := saveTestFunction(t, app, t.TempDir(), "echo", "console.log(1)", "")

		createTestCronJob(t, app, "fresh", "* * * * *", fn.Id, true)

		reportMissedCronRuns(app, now)

		if entries := missedLogs(t, app); len(entries) != 0 {
			t.Errorf("a job created just now produced %d missed entries", len(entries))
		}
	})

	t.Run("schedule not due over the downtime reports nothing", func(t *testing.T) {
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()
		setupFaaSCollections(t, app)
		fn := saveTestFunction(t, app, t.TempDir(), "echo", "console.log(1)", "")

		// Midnight-only schedule, over a ten-minute window that excludes it.
		noon := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
		record := createTestCronJob(t, app, "daily-cron", "0 0 * * *", fn.Id, true)
		setCronJobDate(t, app, record.Id, "lastRunAt", noon.Add(-10*time.Minute))

		reportMissedCronRuns(app, noon)

		if entries := missedLogs(t, app); len(entries) != 0 {
			t.Errorf("a schedule not due over the window produced %d missed entries", len(entries))
		}
	})

	t.Run("capped walk with no occurrence in the window stays silent", func(t *testing.T) {
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()
		setupFaaSCollections(t, app)
		fn := saveTestFunction(t, app, t.TempDir(), "echo", "console.log(1)", "")

		// Yearly schedule, last run 18 months ago: the January 1st of the
		// meantime was genuinely missed, but it predates the 30-day walk. The
		// walk cannot observe it, so nothing is reported — asserting a miss on
		// the truncation flag alone would be a false positive.
		reference := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
		record := createTestCronJob(t, app, "yearly-cron", "0 0 1 1 *", fn.Id, true)
		setCronJobDate(t, app, record.Id, "lastRunAt", time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC))

		reportMissedCronRuns(app, reference)

		if entries := missedLogs(t, app); len(entries) != 0 {
			t.Errorf("a capped walk with no occurrence in the window produced %d missed entries", len(entries))
		}
	})

	t.Run("capped message carries both the walk floor and the last run", func(t *testing.T) {
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()
		setupFaaSCollections(t, app)
		fn := saveTestFunction(t, app, t.TempDir(), "echo", "console.log(1)", "")

		reference := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
		lastRun := reference.Add(-90 * 24 * time.Hour)
		record := createTestCronJob(t, app, "long-outage", "* * * * *", fn.Id, true)
		setCronJobDate(t, app, record.Id, "lastRunAt", lastRun)

		reportMissedCronRuns(app, reference)

		entries := missedLogs(t, app)
		if len(entries) != 1 {
			t.Fatalf("expected exactly 1 missed entry, got %d", len(entries))
		}

		message := decryptedText(app, entries[0], "stderr")
		floor := reference.Add(-maxMissedLookback).Format(types.DefaultDateLayout)
		if !strings.Contains(message, floor) {
			t.Errorf("message does not state the walk floor %q: %s", floor, message)
		}
		if !strings.Contains(message, lastRun.Format(types.DefaultDateLayout)) {
			t.Errorf("message drops the last run date %q: %s", lastRun.Format(types.DefaultDateLayout), message)
		}
		// The counted figure is exact from the floor onwards; only what came
		// before it is unknown.
		if strings.Contains(message, "more than") {
			t.Errorf("message claims an approximation over the counted window: %s", message)
		}
	})
}

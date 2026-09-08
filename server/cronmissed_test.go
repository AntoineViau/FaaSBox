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

			got, capped := countMissedRuns(schedule, time.UTC, tc.since, tc.now)
			if got != tc.want {
				t.Errorf("countMissedRuns() = %d, want %d", got, tc.want)
			}
			if capped {
				t.Error("countMissedRuns() reported a capped walk on a short interval")
			}
		})
	}
}

// TestCountMissedRuns_FollowsTheZone is the reason loc is a parameter: the same
// expression over the same absolute interval does not count the same number of
// occurrences depending on the clock it is read from.
//
// The interval is a single UTC day, and the expression fires once a day at 03:00
// local. Asia/Kolkata is +05:30, so its 03:00 falls at 21:30 UTC the day before —
// inside the walked interval — while 03:00 UTC falls outside it. A count that
// ignored the zone would announce the wrong one.
func TestCountMissedRuns_FollowsTheZone(t *testing.T) {
	schedule, err := cron.NewSchedule("0 3 * * *")
	if err != nil {
		t.Fatal(err)
	}

	kolkata, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatalf("the embedded timezone database did not answer: %v", err)
	}

	// ]04:00 UTC, 23:00 UTC[ on a single day: 03:00 UTC is behind the start, and
	// 03:00 Kolkata is 21:30 UTC, inside.
	since := time.Date(2026, 3, 10, 4, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 10, 23, 0, 0, 0, time.UTC)

	if got, _ := countMissedRuns(schedule, time.UTC, since, now); got != 0 {
		t.Errorf("countMissedRuns() in UTC = %d, want 0", got)
	}
	if got, _ := countMissedRuns(schedule, kolkata, since, now); got != 1 {
		t.Errorf("countMissedRuns() in Asia/Kolkata = %d, want 1", got)
	}
}

// TestCronJobIsDue_ProjectsIntoTheZone pins the absolute minute a schedule fires
// on. "0 3 * * *" set on Asia/Kolkata (+05:30) is due at 21:30 UTC the day
// before, and not at 03:00 UTC — the half-hour offset makes the *minute* differ,
// so nothing here could pass by accident on an hour-aligned zone.
func TestCronJobIsDue_ProjectsIntoTheZone(t *testing.T) {
	schedule, err := cron.NewSchedule("0 3 * * *")
	if err != nil {
		t.Fatal(err)
	}

	kolkata, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatalf("the embedded timezone database did not answer: %v", err)
	}

	due := time.Date(2026, 3, 9, 21, 30, 0, 0, time.UTC)
	if !cronJobIsDue(schedule, kolkata, due) {
		t.Errorf("cronJobIsDue(%s, Asia/Kolkata) = false, want true", due)
	}
	if cronJobIsDue(schedule, time.UTC, due) {
		t.Errorf("cronJobIsDue(%s, UTC) = true, want false", due)
	}

	utcDue := time.Date(2026, 3, 10, 3, 0, 0, 0, time.UTC)
	if cronJobIsDue(schedule, kolkata, utcDue) {
		t.Errorf("cronJobIsDue(%s, Asia/Kolkata) = true, want false — that is the UTC minute", utcDue)
	}
	if !cronJobIsDue(schedule, time.UTC, utcDue) {
		t.Errorf("cronJobIsDue(%s, UTC) = false, want true", utcDue)
	}
}

func TestCountMissedRuns_CappedAtLookback(t *testing.T) {
	schedule, err := cron.NewSchedule("* * * * *")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	since := now.Add(-90 * 24 * time.Hour)

	got, capped := countMissedRuns(schedule, time.UTC, since, now)
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

		record := createTestTrigger(t, app, "missed-cron", "* * * * *", fn.Id, true)
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

		record := createTestTrigger(t, app, "inactive-cron", "* * * * *", fn.Id, false)
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

		record := createTestTrigger(t, app, "never-ran", "* * * * *", fn.Id, true)
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

		createTestTrigger(t, app, "fresh", "* * * * *", fn.Id, true)

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
		record := createTestTrigger(t, app, "daily-cron", "0 0 * * *", fn.Id, true)
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
		record := createTestTrigger(t, app, "yearly-cron", "0 0 1 1 *", fn.Id, true)
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
		record := createTestTrigger(t, app, "long-outage", "* * * * *", fn.Id, true)
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

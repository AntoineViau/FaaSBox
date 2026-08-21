package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// shortStartupDelayUnit collapses the unit a startup delay counts in, on the
// documented model of the depsTimeout seam: proving that a delay of n units is
// waited cannot cost n minutes.
func shortStartupDelayUnit(t testing.TB, d time.Duration) {
	t.Helper()
	previous := startupDelayUnit
	startupDelayUnit = d
	t.Cleanup(func() { startupDelayUnit = previous })
}

// createTestStartupTrigger saves a startup trigger pointing at a function id.
func createTestStartupTrigger(t testing.TB, app core.App, name string, delayMinutes int, functionId string, active bool) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId(faasboxTriggersCollection)
	if err != nil {
		t.Fatal(err)
	}

	record := core.NewRecord(col)
	record.Set("name", name)
	record.Set("kind", "startup")
	record.Set("startupDelayMinutes", delayMinutes)
	record.Set("function", functionId)
	record.Set("active", active)
	if err := app.Save(record); err != nil {
		t.Fatalf("failed to create startup trigger %q: %v", name, err)
	}
	return record
}

// waitForExecutionLog polls for the entries of a function, the run being
// detached. It fails rather than returning short, so a caller never asserts on
// an empty slice that simply had not landed yet.
func waitForExecutionLog(t testing.TB, app core.App, functionName string, want int) []*core.Record {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		entries := executionLogsOf(t, app, functionName)
		if len(entries) >= want {
			return entries
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution logs of %q = %d, want %d", functionName, len(entries), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// startupApp is a test app carrying the collections, an encryption key and a
// stub bun, with one function saved on disk.
func startupApp(t *testing.T) (*tests.TestApp, string, *core.Record) {
	t.Helper()
	withEncryptionKey(t)

	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)

	setupFaaSCollections(t, app)
	bindTriggerHook(app)

	functionsDir := t.TempDir()
	fakeBun(t, "exit 0")
	fn := saveTestFunction(t, app, functionsDir, "booted", "console.log(1)", "")
	return app, functionsDir, fn
}

// TestScheduleStartupRuns_FiresAZeroDelay covers the whole path of a trigger
// that fires as the server comes up: the run happens, the entry says it was a
// startup, and lastRunAt is stamped like on any other trigger.
func TestScheduleStartupRuns_FiresAZeroDelay(t *testing.T) {
	app, functionsDir, fn := startupApp(t)
	trigger := createTestStartupTrigger(t, app, "boot", 0, fn.Id, true)

	scheduleStartupRuns(context.Background(), app, functionsDir)

	entries := waitForExecutionLog(t, app, "booted", 1)
	if got := entries[0].GetString("trigger"); got != "startup" {
		t.Errorf("trigger = %q, want \"startup\"", got)
	}

	stamped, err := app.FindRecordById(faasboxTriggersCollection, trigger.Id)
	if err != nil {
		t.Fatal(err)
	}
	if stamped.GetDateTime("lastRunAt").IsZero() {
		t.Error("lastRunAt was not stamped: a startup run is a run like any other")
	}
}

// TestScheduleStartupRuns_WaitsTheDelay pins that a non-zero delay is actually
// waited before the function runs — measured in the shortened unit.
func TestScheduleStartupRuns_WaitsTheDelay(t *testing.T) {
	app, functionsDir, fn := startupApp(t)
	shortStartupDelayUnit(t, 150*time.Millisecond)
	createTestStartupTrigger(t, app, "later", 2, fn.Id, true)

	scheduleStartupRuns(context.Background(), app, functionsDir)

	// Nothing yet: the delay is two units, so one has not even elapsed.
	time.Sleep(50 * time.Millisecond)
	if got := countExecutionLogs(t, app, "booted"); got != 0 {
		t.Fatalf("execution logs = %d before the delay elapsed, want 0", got)
	}

	waitForExecutionLog(t, app, "booted", 1)
}

// TestScheduleStartupRuns_StopsOnCancel pins that the wait is interrupted by the
// lifecycle context and that nothing runs afterwards. Without the select, a
// shutdown would leave a timer holding a function it is about to execute.
func TestScheduleStartupRuns_StopsOnCancel(t *testing.T) {
	app, functionsDir, fn := startupApp(t)
	shortStartupDelayUnit(t, 200*time.Millisecond)
	createTestStartupTrigger(t, app, "cancelled", 5, fn.Id, true)

	ctx, cancel := context.WithCancel(context.Background())
	scheduleStartupRuns(ctx, app, functionsDir)
	cancel()

	// Well past the point the run would have landed had the wait survived.
	time.Sleep(300 * time.Millisecond)
	if got := countExecutionLogs(t, app, "booted"); got != 0 {
		t.Errorf("execution logs = %d after the context was cancelled, want 0", got)
	}
}

// TestScheduleStartupRuns_SkipsWhatItMustNotArm covers the selection: an
// inactive trigger, a cron trigger and a trigger whose relation no longer
// resolves all leave the pass without a run — the last one with a line saying so.
func TestScheduleStartupRuns_SkipsWhatItMustNotArm(t *testing.T) {
	app, functionsDir, fn := startupApp(t)
	enableServerLogs(t, app)

	createTestStartupTrigger(t, app, "off", 0, fn.Id, false)
	createTestTrigger(t, app, "scheduled", "0 * * * *", fn.Id, true)

	dangling := createTestStartupTrigger(t, app, "orphan", 0, fn.Id, true)
	dangling.Set("function", "doesnotexist000")
	// Straight to SQL: the relation refuses an id that resolves to nothing, and
	// what is under test is precisely how the pass survives one that does not.
	if _, err := app.DB().NewQuery(
		"UPDATE " + faasboxTriggersCollection + " SET function = 'doesnotexist000' WHERE id = {:id}",
	).Bind(map[string]any{"id": dangling.Id}).Execute(); err != nil {
		t.Fatal(err)
	}

	scheduleStartupRuns(context.Background(), app, functionsDir)

	time.Sleep(300 * time.Millisecond)
	if got := countExecutionLogs(t, app, "booted"); got != 0 {
		t.Errorf("execution logs = %d, want none: nothing here was armable", got)
	}

	found := false
	for _, message := range serverLogMessages(t, app) {
		if strings.Contains(message, "trigger points at no function") {
			found = true
		}
	}
	if !found {
		t.Error("the dangling relation was skipped silently: it has to be reported")
	}
}

// TestSyncAllCronJobs_SkipsStartupTriggers pins that a startup trigger never
// reaches the PocketBase scheduler. The blank-schedule guard would drop it
// anyway; the point is that it is dropped on purpose.
func TestSyncAllCronJobs_SkipsStartupTriggers(t *testing.T) {
	app, functionsDir, fn := startupApp(t)
	trigger := createTestStartupTrigger(t, app, "boot", 0, fn.Id, true)

	syncAllCronJobs(app, functionsDir, context.Background())

	for _, registered := range app.Cron().Jobs() {
		if registered.Id() == cronJobPrefix+trigger.Id {
			t.Fatal("a startup trigger was registered on the cron scheduler")
		}
	}
}

// TestReportMissedCronRuns_IgnoresStartupTriggers pins the consequence rather
// than a redundant guard: a startup does not get missed, so no `missed` entry
// may be filed for one. cronmissed.go carries no code for this — its blank
// schedule guard already covers it, and this test is what keeps that true.
func TestReportMissedCronRuns_IgnoresStartupTriggers(t *testing.T) {
	app, _, fn := startupApp(t)
	trigger := createTestStartupTrigger(t, app, "boot", 0, fn.Id, true)
	// Created long ago and never run: a cron trigger in this state reports.
	setCronJobDate(t, app, trigger.Id, "created", time.Now().Add(-48*time.Hour))

	reportMissedCronRuns(app, time.Now())

	if got := countExecutionLogs(t, app, "booted"); got != 0 {
		t.Errorf("execution logs = %d, want none: a startup trigger cannot be missed", got)
	}
}

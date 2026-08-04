package main

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

func TestEnsureCronJobsCollection(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	// First call: should create the collection
	if err := ensureCronJobsCollection(app); err != nil {
		t.Fatalf("first call failed: %v", err)
	}

	col, err := app.FindCollectionByNameOrId(faasboxCronJobsCollection)
	if err != nil {
		t.Fatalf("collection not found after creation: %v", err)
	}

	// Verify expected fields exist
	expectedFields := []string{"name", "schedule", "functionName", "payload", "active", "maxQueue", "lastRunAt"}
	for _, fieldName := range expectedFields {
		if col.Fields.GetByName(fieldName) == nil {
			t.Errorf("field %q not found in collection", fieldName)
		}
	}

	// Verify required fields
	nameField := col.Fields.GetByName("name")
	if nameField != nil {
		if tf, ok := nameField.(*core.TextField); ok && !tf.Required {
			t.Error("name field should be required")
		}
	}
	scheduleField := col.Fields.GetByName("schedule")
	if scheduleField != nil {
		if tf, ok := scheduleField.(*core.TextField); ok && !tf.Required {
			t.Error("schedule field should be required")
		}
	}
	functionNameField := col.Fields.GetByName("functionName")
	if functionNameField != nil {
		if tf, ok := functionNameField.(*core.TextField); ok && !tf.Required {
			t.Error("functionName field should be required")
		}
	}

	// Second call: should be idempotent (no error)
	if err := ensureCronJobsCollection(app); err != nil {
		t.Fatalf("second call (idempotent) failed: %v", err)
	}
}

func TestEnsureCronJobsCollection_Migration(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	// Create collection without maxQueue (simulate old schema)
	col := core.NewBaseCollection(faasboxCronJobsCollection)
	col.Fields.Add(
		&core.TextField{Name: "name", Required: true},
		&core.TextField{Name: "schedule", Required: true},
		&core.TextField{Name: "functionName", Required: true},
		&core.JSONField{Name: "payload"},
		&core.BoolField{Name: "active"},
	)
	if err := app.Save(col); err != nil {
		t.Fatal(err)
	}

	// ensureCronJobsCollection should add every missing field in one pass
	if err := ensureCronJobsCollection(app); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	col, _ = app.FindCollectionByNameOrId(faasboxCronJobsCollection)
	for _, fieldName := range []string{"maxQueue", "lastRunAt"} {
		if col.Fields.GetByName(fieldName) == nil {
			t.Errorf("%s field not added during migration", fieldName)
		}
	}
}

func TestValidateCronScheduleHook(t *testing.T) {
	// A test app starts with no hook bound: wire them the way main.go does.
	bindCronHooks := func(app core.App) {
		app.OnRecordCreate(faasboxCronJobsCollection).BindFunc(validateCronScheduleHook)
		app.OnRecordUpdate(faasboxCronJobsCollection).BindFunc(validateCronScheduleHook)
	}

	scenarios := []tests.ApiScenario{
		{
			Name:   "invalid expression refused with a message the client can display",
			Method: http.MethodPost,
			URL:    "/api/collections/" + faasboxCronJobsCollection + "/records",
			Body: strings.NewReader(
				`{"name":"bad-schedule","schedule":"0 0 0 * *","functionName":"echo","active":true}`,
			),
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				bindCronHooks(app)
			},
			ExpectedStatus: 400,
			// What is under test is the wording reaching the wire: an ordinary
			// error would be swallowed and replaced by "Failed to create record",
			// leaving the editor with nothing to show.
			ExpectedContent: []string{
				`Invalid cron expression \"0 0 0 * *\"`,
				`minute hour day-of-month month day-of-week`,
			},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if _, err := app.FindFirstRecordByData(faasboxCronJobsCollection, "name", "bad-schedule"); err == nil {
					t.Error("record was created despite the rejected schedule")
				}
			},
		},
		{
			Name:   "valid expression goes through",
			Method: http.MethodPost,
			URL:    "/api/collections/" + faasboxCronJobsCollection + "/records",
			Body: strings.NewReader(
				`{"name":"good-schedule","schedule":"0 0 * * *","functionName":"echo","active":true}`,
			),
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				bindCronHooks(app)
			},
			ExpectedStatus:  200,
			ExpectedContent: []string{`"schedule":"0 0 * * *"`},
		},
	}

	for _, s := range scenarios {
		s.Test(t)
	}
}

func TestRunFunction_MaxQueue(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	// Reset cronQueueDepth between tests
	cronQueueDepth.Range(func(key, _ any) bool {
		cronQueueDepth.Delete(key)
		return true
	})

	t.Run("maxQueue=0 does not limit", func(t *testing.T) {
		// Simulate 3 in-flight executions by pre-loading the counter
		val, _ := cronQueueDepth.LoadOrStore("unlimited-func", &atomic.Int32{})
		counter := val.(*atomic.Int32)
		counter.Store(0)

		// With maxQueue=0, runFunction should not check the counter at all.
		// We can't easily test full execution without bun, but we can verify
		// the counter is NOT incremented (maxQueue=0 skips the guard).
		before := counter.Load()
		// Call with a non-existent function — it will fail at execution but
		// should pass the queue check.
		runFunction(context.Background(), app, t.TempDir(), "unlimited-func", "{}", 0, "")
		after := counter.Load()

		if before != after {
			t.Error("maxQueue=0 should not touch the queue depth counter")
		}
	})

	t.Run("maxQueue=2 skips when full", func(t *testing.T) {
		// Pre-load counter at 2 (simulating 2 already in-flight)
		val, _ := cronQueueDepth.LoadOrStore("limited-func", &atomic.Int32{})
		counter := val.(*atomic.Int32)
		counter.Store(2)

		// With maxQueue=2 and depth already at 2, a new call should be skipped.
		// The counter will be incremented to 3 then checked > 2, so it returns early.
		runFunction(context.Background(), app, t.TempDir(), "limited-func", "{}", 2, "")

		// Counter should be back to 2 (incremented to 3, then decremented by defer)
		if got := counter.Load(); got != 2 {
			t.Errorf("counter should be back to 2 after skip, got %d", got)
		}
	})
}

func TestSyncAllCronJobs_EmptyCollection(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	// Should not panic with empty collection
	syncAllCronJobs(app, t.TempDir(), context.Background())
}

func TestSyncAllCronJobs_WithRecords(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	// Create a cron job record
	col, err := app.FindCollectionByNameOrId(faasboxCronJobsCollection)
	if err != nil {
		t.Fatal(err)
	}

	record := core.NewRecord(col)
	record.Set("name", "test-cron")
	record.Set("schedule", "*/5 * * * *")
	record.Set("functionName", "echo")
	record.Set("payload", `{"test": true}`)
	record.Set("active", true)
	if err := app.Save(record); err != nil {
		t.Fatalf("failed to create cron record: %v", err)
	}

	functionsDir := setupTestFunctionsDir(t, map[string]string{"echo": ""})

	// Should register the cron job without error
	syncAllCronJobs(app, functionsDir, context.Background())

	// Verify the job was registered
	jobId := cronJobPrefix + record.Id
	found := false
	for _, job := range app.Cron().Jobs() {
		if job.Id() == jobId {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("cron job %q was not registered", jobId)
	}
}

func TestSyncAllCronJobs_SkipsInactive(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	record := createTestCronJob(t, app, "inactive-cron", "*/5 * * * *", "echo", false)

	syncAllCronJobs(app, t.TempDir(), context.Background())

	// Verify the job was NOT registered
	jobId := cronJobPrefix + record.Id
	for _, job := range app.Cron().Jobs() {
		if job.Id() == jobId {
			t.Errorf("inactive cron job %q should not be registered", jobId)
		}
	}
}

func TestRunFunction_StampsLastRunAt(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	record := createTestCronJob(t, app, "stamped-cron", "* * * * *", "missing-func", true)

	// The function does not exist: the execution fails, and the stamp must still
	// be written — what it records is that the trigger fired.
	runFunction(context.Background(), app, t.TempDir(), "missing-func", "{}", 0, record.Id)

	updated, err := app.FindRecordById(faasboxCronJobsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if updated.GetDateTime("lastRunAt").IsZero() {
		t.Error("lastRunAt was not stamped after a failed cron execution")
	}
}

// TestRunFunction_PublishesDependencyState is the cron half of the parity: the
// safety net publishes what it did whichever path called the engine, because the
// engine itself cannot — it holds no core.App.
func TestRunFunction_PublishesDependencyState(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	functionsDir := t.TempDir()
	fakeBun(t, `echo "error: package nope@1.0.0 not found" >&2`+"\nexit 1")
	record := saveTestFunction(t, app, functionsDir, "cron-broken-deps",
		"console.log('hi')", `{"dependencies":{"nope":"1.0.0"}}`)

	runFunction(context.Background(), app, functionsDir, "cron-broken-deps", "{}", 0, "")

	stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.GetString("depsStatus"); got != depsStatusError {
		t.Errorf("depsStatus = %q, want %q", got, depsStatusError)
	}
	if got := stored.GetString("depsError"); !strings.Contains(got, "nope@1.0.0 not found") {
		t.Errorf("depsError = %q, want the install output", got)
	}
}

func TestRunFunction_SafetyNetPublishesReady(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	functionsDir := t.TempDir()
	fakeBun(t, "exit 0")
	record := saveTestFunction(t, app, functionsDir, "cron-fresh-deps",
		"console.log('hi')", `{"dependencies":{"left-pad":"1.0.0"}}`)
	setDepsState(app, record.Id, "cron-fresh-deps", depsStatusPending, "")

	runFunction(context.Background(), app, functionsDir, "cron-fresh-deps", "{}", 0, "")

	stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.GetString("depsStatus"); got != depsStatusReady {
		t.Errorf("depsStatus = %q, want %q after the safety net installed", got, depsStatusReady)
	}
}

// TestRunFunction_SafetyNetPersistsLockfile is the cron half of the parity, and the
// path where it matters most: a nightly trigger is often what discovers the install
// is due, so it must pin its result like every other caller.
func TestRunFunction_SafetyNetPersistsLockfile(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	functionsDir := t.TempDir()
	fakeBun(t, "mkdir -p node_modules\necho resolved-by-cron > bun.lock\nexit 0")
	record := saveTestFunction(t, app, functionsDir, "cron-pins",
		"console.log('hi')", `{"dependencies":{"dayjs":"^1.11.0"}}`)

	runFunction(context.Background(), app, functionsDir, "cron-pins", "{}", 0, "")

	stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stored.GetString("bunLock")); got != "resolved-by-cron" {
		t.Errorf("bunLock = %q, want what the safety net resolved", got)
	}
}

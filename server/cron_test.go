package main

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

func TestEnsureTriggersCollection(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	// The relation needs the collection it points at to exist first.
	if err := ensureFunctionsCollection(app); err != nil {
		t.Fatal(err)
	}

	// First call: should create the collection
	if err := ensureTriggersCollection(app); err != nil {
		t.Fatalf("first call failed: %v", err)
	}

	col, err := app.FindCollectionByNameOrId(faasboxTriggersCollection)
	if err != nil {
		t.Fatalf("collection not found after creation: %v", err)
	}

	// Verify expected fields exist
	expectedFields := []string{
		"name", "schedule", "function", "payload", "active", "maxQueue", "lastRunAt",
		"kind", "startupDelayMinutes",
	}
	for _, fieldName := range expectedFields {
		if col.Fields.GetByName(fieldName) == nil {
			t.Errorf("field %q not found in collection", fieldName)
		}
	}

	// The name it used to carry is gone: it was never a reference, and a rename
	// left every trigger firing into the void without a word.
	if col.Fields.GetByName("functionName") != nil {
		t.Error("functionName is still on the collection: the relation replaces it")
	}

	// Verify required fields
	nameField := col.Fields.GetByName("name")
	if nameField != nil {
		if tf, ok := nameField.(*core.TextField); ok && !tf.Required {
			t.Error("name field should be required")
		}
	}
	// Not required any more: a startup trigger carries no expression. The refusal
	// of a blank one on a cron trigger belongs to validateTriggerHook, which is
	// the only place that knows which kind it is weighing.
	scheduleField := col.Fields.GetByName("schedule")
	if tf, ok := scheduleField.(*core.TextField); ok && tf.Required {
		t.Error("schedule is still Required: a startup trigger could not be saved")
	}

	kind, ok := col.Fields.GetByName("kind").(*core.SelectField)
	if !ok {
		t.Fatalf("kind is a %T, want a *core.SelectField", col.Fields.GetByName("kind"))
	}
	if kind.Required {
		t.Error("kind is Required: an empty column has to stay writable, it reads as \"cron\"")
	}
	if kind.MaxSelect != 1 {
		t.Errorf("kind MaxSelect = %d, want 1", kind.MaxSelect)
	}
	if len(kind.Values) != 2 || kind.Values[0] != "cron" || kind.Values[1] != "startup" {
		t.Errorf("kind values = %v, want [cron startup]", kind.Values)
	}

	// In the clear, like active, maxQueue and lastRunAt: a discriminant and a
	// delay say nothing of the business content.
	for _, name := range []string{"kind", "startupDelayMinutes"} {
		if slices.Contains(encryptedTextFields[faasboxTriggersCollection], name) {
			t.Errorf("%s is listed as an encrypted column", name)
		}
	}

	functions, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
	if err != nil {
		t.Fatal(err)
	}
	rel, ok := col.Fields.GetByName("function").(*core.RelationField)
	if !ok {
		t.Fatalf("function is a %T, want a *core.RelationField", col.Fields.GetByName("function"))
	}
	if rel.CollectionId != functions.Id {
		t.Errorf("function points at collection %q, want %q", rel.CollectionId, functions.Id)
	}
	if !rel.Required {
		t.Error("a trigger with no function has nothing to fire: the relation must be required")
	}
	if rel.MaxSelect != 1 {
		t.Errorf("MaxSelect = %d, want 1", rel.MaxSelect)
	}
	if !rel.CascadeDelete {
		t.Error("CascadeDelete is off: deleting a function would leave its triggers behind")
	}

	// Second call: should be idempotent (no error)
	if err := ensureTriggersCollection(app); err != nil {
		t.Fatalf("second call (idempotent) failed: %v", err)
	}
}

// bindTriggerHook wires the trigger guard the way main.go does. A test app
// starts with no hook bound — and since a blank schedule is refused here rather
// than by the field, an app that skips it accepts a trigger the server would not.
func bindTriggerHook(app core.App) {
	app.OnRecordCreate(faasboxTriggersCollection).BindFunc(validateTriggerHook)
	app.OnRecordUpdate(faasboxTriggersCollection).BindFunc(validateTriggerHook)
}

func TestValidateTriggerHook(t *testing.T) {
	bindCronHooks := bindTriggerHook

	scenarios := []tests.ApiScenario{
		{
			Name:   "invalid expression refused with a message the client can display",
			Method: http.MethodPost,
			URL:    "/api/collections/" + faasboxTriggersCollection + "/records",
			Body: strings.NewReader(
				`{"name":"bad-schedule","schedule":"0 0 0 * *","function":"echofunction001","active":true}`,
			),
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				saveTestFunctionAs(t, app, t.TempDir(), testFunctionId, "echo", "console.log(1)", "")
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
				if _, err := app.FindFirstRecordByData(faasboxTriggersCollection, "name", "bad-schedule"); err == nil {
					t.Error("record was created despite the rejected schedule")
				}
			},
		},
		{
			Name:   "valid expression goes through",
			Method: http.MethodPost,
			URL:    "/api/collections/" + faasboxTriggersCollection + "/records",
			Body: strings.NewReader(
				`{"name":"good-schedule","schedule":"0 0 * * *","function":"echofunction001","active":true}`,
			),
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				saveTestFunctionAs(t, app, t.TempDir(), testFunctionId, "echo", "console.log(1)", "")
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

// TestTriggerKind_EmptyReadsAsCron pins the single point of normalisation. An
// empty column is the shape every record had before startup triggers existed,
// and the one the PocketBase admin writes when it leaves the select untouched.
func TestTriggerKind_EmptyReadsAsCron(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	col, err := app.FindCollectionByNameOrId(faasboxTriggersCollection)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(col)

	if got := triggerKind(record); got != "cron" {
		t.Errorf("triggerKind on an empty column = %q, want \"cron\"", got)
	}
	record.Set("kind", "startup")
	if got := triggerKind(record); got != "startup" {
		t.Errorf("triggerKind = %q, want \"startup\"", got)
	}
	record.Set("kind", "cron")
	if got := triggerKind(record); got != "cron" {
		t.Errorf("triggerKind = %q, want \"cron\"", got)
	}
}

// TestValidateTriggerHook_ByKind covers the three rules the kind brings: a cron
// trigger needs an expression, a startup trigger must not carry one, and its
// delay is a whole number of minutes within bounds.
func TestValidateTriggerHook_ByKind(t *testing.T) {
	// Every case posts one record and reads the outcome off the status: what is
	// under test is what the hook lets through, and the wording that comes back
	// when it does not.
	cases := []struct {
		name    string
		body    string
		status  int
		content []string
	}{
		{
			name:    "cron trigger with a blank schedule is refused",
			body:    `{"name":"blank","schedule":"","function":"echofunction001","active":true}`,
			status:  400,
			content: []string{`A cron trigger needs a schedule`},
		},
		{
			name:    "kind left empty is weighed as a cron trigger",
			body:    `{"name":"implicit","function":"echofunction001","active":true}`,
			status:  400,
			content: []string{`A cron trigger needs a schedule`},
		},
		{
			name:    "startup trigger carrying a schedule is refused",
			body:    `{"name":"both","kind":"startup","schedule":"0 * * * *","function":"echofunction001","active":true}`,
			status:  400,
			content: []string{`A startup trigger carries no schedule`},
		},
		{
			name:    "startup delay past the bound is refused",
			body:    `{"name":"far","kind":"startup","startupDelayMinutes":1440,"function":"echofunction001","active":true}`,
			status:  400,
			content: []string{`Invalid startup delay`, `between 0 and 1439`},
		},
		{
			name:    "negative startup delay is refused",
			body:    `{"name":"back","kind":"startup","startupDelayMinutes":-1,"function":"echofunction001","active":true}`,
			status:  400,
			content: []string{`Invalid startup delay`},
		},
		{
			name:    "fractional startup delay is refused",
			body:    `{"name":"half","kind":"startup","startupDelayMinutes":3.5,"function":"echofunction001","active":true}`,
			status:  400,
			content: []string{`Invalid startup delay`},
		},
		{
			name:    "startup trigger with no schedule goes through",
			body:    `{"name":"boot","kind":"startup","startupDelayMinutes":5,"function":"echofunction001","active":true}`,
			status:  200,
			content: []string{`"kind":"startup"`, `"startupDelayMinutes":5`},
		},
		{
			name:    "the bound itself goes through",
			body:    `{"name":"edge","kind":"startup","startupDelayMinutes":1439,"function":"echofunction001","active":true}`,
			status:  200,
			content: []string{`"startupDelayMinutes":1439`},
		},
	}

	for _, c := range cases {
		s := tests.ApiScenario{
			Name:   c.name,
			Method: http.MethodPost,
			URL:    "/api/collections/" + faasboxTriggersCollection + "/records",
			Body:   strings.NewReader(c.body),
			Headers: map[string]string{
				"Authorization": superuserToken,
			},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				saveTestFunctionAs(t, app, t.TempDir(), testFunctionId, "echo", "console.log(1)", "")
				bindTriggerHook(app)
			},
			ExpectedStatus:  c.status,
			ExpectedContent: c.content,
		}
		s.Test(t)
	}
}

func TestRunFunction_MaxQueue(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	// Reset triggerQueueDepth between tests
	triggerQueueDepth.Range(func(key, _ any) bool {
		triggerQueueDepth.Delete(key)
		return true
	})

	t.Run("maxQueue=0 does not limit", func(t *testing.T) {
		// Simulate 3 in-flight executions by pre-loading the counter
		val, _ := triggerQueueDepth.LoadOrStore("unlimited-func", &atomic.Int32{})
		counter := val.(*atomic.Int32)
		counter.Store(0)

		// With maxQueue=0, runFunction should not check the counter at all.
		// We can't easily test full execution without bun, but we can verify
		// the counter is NOT incremented (maxQueue=0 skips the guard).
		before := counter.Load()
		// Call with a non-existent function — it will fail at execution but
		// should pass the queue check.
		runFunction(context.Background(), app, t.TempDir(), "unlimited-func", newTriggerInput(triggerCron, "test trigger", "{}"), 0, "")
		after := counter.Load()

		if before != after {
			t.Error("maxQueue=0 should not touch the queue depth counter")
		}
	})

	t.Run("maxQueue=2 skips when full", func(t *testing.T) {
		// Pre-load counter at 2 (simulating 2 already in-flight)
		val, _ := triggerQueueDepth.LoadOrStore("limited-func", &atomic.Int32{})
		counter := val.(*atomic.Int32)
		counter.Store(2)

		// With maxQueue=2 and depth already at 2, a new call should be skipped.
		// The counter will be incremented to 3 then checked > 2, so it returns early.
		runFunction(context.Background(), app, t.TempDir(), "limited-func", newTriggerInput(triggerCron, "test trigger", "{}"), 2, "")

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
	col, err := app.FindCollectionByNameOrId(faasboxTriggersCollection)
	if err != nil {
		t.Fatal(err)
	}

	functionsDir := t.TempDir()
	fn := saveTestFunction(t, app, functionsDir, "echo", "console.log(1)", "")

	record := core.NewRecord(col)
	record.Set("name", "test-cron")
	record.Set("schedule", "*/5 * * * *")
	record.Set("function", fn.Id)
	record.Set("payload", `{"test": true}`)
	record.Set("active", true)
	if err := app.Save(record); err != nil {
		t.Fatalf("failed to create cron record: %v", err)
	}

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

	functionsDir := t.TempDir()
	fn := saveTestFunction(t, app, functionsDir, "echo", "console.log(1)", "")
	record := createTestTrigger(t, app, "inactive-cron", "*/5 * * * *", fn.Id, false)

	syncAllCronJobs(app, functionsDir, context.Background())

	// Verify the job was NOT registered
	jobId := cronJobPrefix + record.Id
	for _, job := range app.Cron().Jobs() {
		if job.Id() == jobId {
			t.Errorf("inactive cron job %q should not be registered", jobId)
		}
	}
}

// hasCronJob reports whether the scheduler currently holds the job of a cron
// record.
func hasCronJob(app core.App, recordId string) bool {
	for _, job := range app.Cron().Jobs() {
		if job.Id() == cronJobPrefix+recordId {
			return true
		}
	}
	return false
}

// TestDeleteFunction_CascadesToItsTriggers is what the relation buys over the
// name it replaces: no cleanup code of ours runs here, and none needs to.
//
// The scheduler is checked too, and that half is not obvious. The cascade
// deletes the cron records through app.Delete, so each fires
// OnRecordAfterDeleteSuccess — but PocketBase defers those to the end of the
// transaction, and the resync they trigger must therefore read a database the
// jobs have already left. The hooks are bound here the way main.go binds them,
// since a test app starts with none.
func TestDeleteFunction_CascadesToItsTriggers(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	functionsDir := t.TempDir()
	app.OnRecordAfterDeleteSuccess(faasboxTriggersCollection).BindFunc(func(e *core.RecordEvent) error {
		syncAllCronJobs(e.App, functionsDir, context.Background())
		return e.Next()
	})

	doomed := saveTestFunction(t, app, functionsDir, "doomed", "console.log(1)", "")
	survivor := saveTestFunction(t, app, functionsDir, "survivor", "console.log(1)", "")
	mine := createTestTrigger(t, app, "mine", "* * * * *", doomed.Id, true)
	theirs := createTestTrigger(t, app, "theirs", "* * * * *", survivor.Id, true)

	syncAllCronJobs(app, functionsDir, context.Background())
	if !hasCronJob(app, mine.Id) || !hasCronJob(app, theirs.Id) {
		t.Fatal("the two triggers were not registered to begin with")
	}

	if err := app.Delete(doomed); err != nil {
		t.Fatal(err)
	}

	if _, err := app.FindRecordById(faasboxTriggersCollection, mine.Id); err == nil {
		t.Error("the trigger of the deleted function is still there")
	}
	if _, err := app.FindRecordById(faasboxTriggersCollection, theirs.Id); err != nil {
		t.Errorf("the trigger of another function was swept away too: %v", err)
	}

	if hasCronJob(app, mine.Id) {
		t.Error("the scheduler still holds the job of the deleted function")
	}
	if !hasCronJob(app, theirs.Id) {
		t.Error("the resync dropped the job of a function nobody deleted")
	}
}

// TestSyncAllCronJobs_SurvivesARename pins the other half of the rule: the
// scheduler is only rebuilt when a *cron* record changes, so a job registered
// before a rename must still find its function when it fires.
func TestSyncAllCronJobs_SurvivesARename(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	functionsDir := t.TempDir()
	fakeBun(t, "exit 0")
	fn := saveTestFunction(t, app, functionsDir, "before", "console.log(1)", "")
	job := createTestTrigger(t, app, "nightly", "* * * * *", fn.Id, true)

	syncAllCronJobs(app, functionsDir, context.Background())

	fn.Set("name", "after")
	if err := app.Save(fn); err != nil {
		t.Fatal(err)
	}

	// The registration stands, untouched by a change on the other collection.
	jobId := cronJobPrefix + job.Id
	found := false
	for _, registered := range app.Cron().Jobs() {
		if registered.Id() == jobId {
			found = true
		}
	}
	if !found {
		t.Fatalf("cron job %q is no longer registered after the rename", jobId)
	}

	// And firing it reaches the function under its new name.
	runFunction(context.Background(), app, functionsDir, fn.Id, newTriggerInput(triggerCron, "test trigger", "{}"), 0, job.Id)

	entries, err := app.FindAllRecords(faasboxLogsCollection)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("faasbox_logs holds %d entries, want the one the run wrote", len(entries))
	}
	if got := decryptedText(app, entries[0], "functionName"); got != "after" {
		t.Errorf("functionName = %q, want the name the function carried when it ran", got)
	}
}

func TestRunFunction_StampsLastRunAt(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	// The record exists but its script never reached the disk, so the execution
	// fails — and the stamp must still be written, since what it records is that
	// the trigger fired.
	fn := saveTestFunction(t, app, t.TempDir(), "missing-func", "console.log(1)", "")
	record := createTestTrigger(t, app, "stamped-cron", "* * * * *", fn.Id, true)

	runFunction(context.Background(), app, t.TempDir(), fn.Id, newTriggerInput(triggerCron, "test trigger", "{}"), 0, record.Id)

	updated, err := app.FindRecordById(faasboxTriggersCollection, record.Id)
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

	runFunction(context.Background(), app, functionsDir, record.Id, newTriggerInput(triggerCron, "test trigger", "{}"), 0, "")

	stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.GetString("depsStatus"); got != depsStatusError {
		t.Errorf("depsStatus = %q, want %q", got, depsStatusError)
	}
	if got := functionDepsError(app, stored); !strings.Contains(got, "nope@1.0.0 not found") {
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

	runFunction(context.Background(), app, functionsDir, record.Id, newTriggerInput(triggerCron, "test trigger", "{}"), 0, "")

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

	runFunction(context.Background(), app, functionsDir, record.Id, newTriggerInput(triggerCron, "test trigger", "{}"), 0, "")

	stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(functionBunLock(app, stored)); got != "resolved-by-cron" {
		t.Errorf("bunLock = %q, want what the safety net resolved", got)
	}
}

package main

import (
	"context"
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
	expectedFields := []string{"name", "schedule", "functionName", "payload", "active", "maxQueue"}
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

	// ensureCronJobsCollection should add the missing maxQueue field
	if err := ensureCronJobsCollection(app); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	col, _ = app.FindCollectionByNameOrId(faasboxCronJobsCollection)
	if col.Fields.GetByName("maxQueue") == nil {
		t.Error("maxQueue field not added during migration")
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
		runFunction(context.Background(), app, t.TempDir(), "unlimited-func", "{}", 0)
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
		runFunction(context.Background(), app, t.TempDir(), "limited-func", "{}", 2)

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

	col, err := app.FindCollectionByNameOrId(faasboxCronJobsCollection)
	if err != nil {
		t.Fatal(err)
	}

	record := core.NewRecord(col)
	record.Set("name", "inactive-cron")
	record.Set("schedule", "*/5 * * * *")
	record.Set("functionName", "echo")
	record.Set("active", false)
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}

	syncAllCronJobs(app, t.TempDir(), context.Background())

	// Verify the job was NOT registered
	jobId := cronJobPrefix + record.Id
	for _, job := range app.Cron().Jobs() {
		if job.Id() == jobId {
			t.Errorf("inactive cron job %q should not be registered", jobId)
		}
	}
}

package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/logger"
	"github.com/pocketbase/pocketbase/tools/types"
)

// Pre-signed superuser JWT from PocketBase's default test data.
const superuserToken = "eyJhbGciOiJIUzI1NiJ9.eyJpZCI6InN5d2JoZWNuaDQ2cmhtMCIsInR5cGUiOiJhdXRoIiwiY29sbGVjdGlvbklkIjoicGJjXzMxNDI2MzU4MjMiLCJleHAiOjI1MjQ2MDQ0NjEsInJlZnJlc2hhYmxlIjp0cnVlfQ.UXgO3j-0BumcugrFjbd7j0M4MQvbrLggLlcu_YNGjoY"

// setupFaaSCollections creates the faasbox_api_keys and faasbox_cron_jobs collections on the test app.
func setupFaaSCollections(t testing.TB, app core.App) {
	t.Helper()
	if err := ensureAPIKeysCollection(app); err != nil {
		t.Fatalf("failed to create API keys collection: %v", err)
	}
	if err := ensureCronJobsCollection(app); err != nil {
		t.Fatalf("failed to create cron jobs collection: %v", err)
	}
	if err := ensureLogsCollection(app); err != nil {
		t.Fatalf("failed to create logs collection: %v", err)
	}
}

// createTestAPIKey creates an API key record and returns the raw key string.
func createTestAPIKey(t testing.TB, app core.App, name string, allowedFunctions []string) string {
	t.Helper()
	rawKey, err := generateAPIKey(app, name, allowedFunctions, types.DateTime{})
	if err != nil {
		t.Fatalf("failed to generate API key %q: %v", name, err)
	}
	return rawKey
}

// createTestAPIKeyWithOptions creates an API key with custom active/expiry settings.
func createTestAPIKeyWithOptions(t testing.TB, app core.App, name string, allowedFunctions []string, active bool, expiresAt time.Time) string {
	t.Helper()
	rawKey := createTestAPIKey(t, app, name, allowedFunctions)

	// Find and update the record
	hash := hashAPIKey(rawKey)
	record, err := app.FindFirstRecordByData(faasboxAPIKeysCollection, "keyHash", hash)
	if err != nil {
		t.Fatalf("failed to find API key record: %v", err)
	}
	record.Set("active", active)
	if !expiresAt.IsZero() {
		record.Set("expiresAt", expiresAt.UTC().Format("2006-01-02 15:04:05.000Z"))
	}
	if err := app.Save(record); err != nil {
		t.Fatalf("failed to update API key record: %v", err)
	}
	return rawKey
}

// setupTestFunctionsDir creates a temp directory with function stubs.
// funcs maps function name to index.ts content (empty string = minimal stub).
func setupTestFunctionsDir(t testing.TB, funcs map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range funcs {
		funcDir := filepath.Join(dir, name)
		if err := os.MkdirAll(funcDir, 0o755); err != nil {
			t.Fatalf("failed to create function dir %q: %v", name, err)
		}
		if content == "" {
			content = `const payload = await Bun.stdin.text(); console.log(JSON.stringify({echo: payload}));`
		}
		if err := os.WriteFile(filepath.Join(funcDir, "index.ts"), []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write index.ts for %q: %v", name, err)
		}
	}
	return dir
}

// saveTestFunction stores a faasbox_functions record and mirrors it to disk,
// reproducing what the create/update hooks do before the dependency install is
// scheduled. An empty pkg means the function declares no dependencies.
func saveTestFunction(t testing.TB, app core.App, functionsDir, name, script, pkg string) *core.Record {
	t.Helper()
	if err := ensureFunctionsCollection(app); err != nil {
		t.Fatalf("failed to create functions collection: %v", err)
	}
	col, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
	if err != nil {
		t.Fatal(err)
	}

	record, err := app.FindFirstRecordByData(faasboxFunctionsCollection, "name", name)
	if err != nil {
		record = core.NewRecord(col)
		record.Set("name", name)
	}
	record.Set("script", script)
	record.Set("packageJson", pkg)
	if err := app.Save(record); err != nil {
		t.Fatalf("failed to save function %q: %v", name, err)
	}

	if err := syncRecordToDisk(record, functionsDir); err != nil {
		t.Fatalf("failed to sync function %q to disk: %v", name, err)
	}
	return record
}

// waitDepsStatus polls the record until depsStatus reaches want, and returns the
// record in that state.
func waitDepsStatus(t testing.TB, app core.App, recordId, want string) *core.Record {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	got := ""
	for time.Now().Before(deadline) {
		record, err := app.FindRecordById(faasboxFunctionsCollection, recordId)
		if err == nil {
			got = record.GetString("depsStatus")
			if got == want {
				return record
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("depsStatus = %q, want %q", got, want)
	return nil
}

// enableServerLogs makes app.Logger() actually persist what it is handed. The
// batch handler drops every record when Logs.MaxDays is 0, which is how the test
// app ships — call this before the code under test logs anything.
func enableServerLogs(t testing.TB, app core.App) {
	t.Helper()
	if app.Settings().Logs.MaxDays > 0 {
		return
	}
	app.Settings().Logs.MaxDays = 7
	if err := app.Save(app.Settings()); err != nil {
		t.Fatalf("failed to enable server logs: %v", err)
	}
}

// serverLogMessages returns the messages written through app.Logger().
//
// The handler batches by size and by a three-second ticker, neither of which a
// test should wait for, so it is flushed first.
func serverLogMessages(t testing.TB, app core.App) []string {
	t.Helper()
	handler, ok := app.Logger().Handler().(*logger.BatchHandler)
	if !ok {
		t.Fatalf("the app logger is a %T, expected a batch handler to flush", app.Logger().Handler())
	}
	if err := handler.WriteAll(context.Background()); err != nil {
		t.Fatalf("failed to flush the log handler: %v", err)
	}

	logs := []*core.Log{}
	if err := app.LogQuery().All(&logs); err != nil {
		t.Fatalf("failed to read the server logs: %v", err)
	}
	messages := make([]string, len(logs))
	for i, l := range logs {
		messages[i] = l.Message
	}
	return messages
}

// countExecutionLogs returns how many faasbox_logs entries a function carries.
func countExecutionLogs(t testing.TB, app core.App, functionName string) int {
	t.Helper()
	records, err := app.FindAllRecords(faasboxLogsCollection,
		dbx.HashExp{"functionName": functionName})
	if err != nil {
		t.Fatalf("failed to read the execution logs of %q: %v", functionName, err)
	}
	return len(records)
}

// registerFaaSRoutes registers the FaaS HTTP routes on the test server's router.
func registerFaaSRoutes(app *tests.TestApp, e *core.ServeEvent, functionsDir string) {
	// Health check (public)
	e.Router.GET("/health", func(re *core.RequestEvent) error {
		return re.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Routes protected by API key
	faas := e.Router.Group("")
	faas.Bind(requireAPIKey(e.App))
	faas.POST("/invoke/{name}", func(re *core.RequestEvent) error {
		return invokeHandler(re, functionsDir)
	})
	faas.GET("/functions", func(re *core.RequestEvent) error {
		return listFunctionsHandler(re, functionsDir)
	})

	// Key management (superuser only)
	e.Router.POST("/api/faasbox/keys", func(re *core.RequestEvent) error {
		return createKeyHandler(re)
	}).Bind(apis.RequireSuperuserAuth())

	// Decrypted environment of a function (superuser only)
	e.Router.GET("/api/faasbox/functions/{name}/env", func(re *core.RequestEvent) error {
		return functionEnvHandler(re)
	}).Bind(apis.RequireSuperuserAuth())
}

// setCronJobDate overwrites a date column PocketBase manages itself (created) or
// that is only ever written by direct SQL (lastRunAt).
func setCronJobDate(t testing.TB, app core.App, recordId, column string, at time.Time) {
	t.Helper()
	_, err := app.DB().NewQuery(
		"UPDATE " + faasboxCronJobsCollection + " SET " + column + " = {:at} WHERE id = {:id}",
	).Bind(dbx.Params{
		"at": at.UTC().Format(types.DefaultDateLayout),
		"id": recordId,
	}).Execute()
	if err != nil {
		t.Fatalf("failed to set %s on %s: %v", column, recordId, err)
	}
}

// createTestCronJob saves a cron job record and returns it.
func createTestCronJob(t testing.TB, app core.App, name, schedule, functionName string, active bool) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId(faasboxCronJobsCollection)
	if err != nil {
		t.Fatal(err)
	}

	record := core.NewRecord(col)
	record.Set("name", name)
	record.Set("schedule", schedule)
	record.Set("functionName", functionName)
	record.Set("active", active)
	if err := app.Save(record); err != nil {
		t.Fatalf("failed to create cron record %q: %v", name, err)
	}
	return record
}

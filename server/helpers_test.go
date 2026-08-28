package main

import (
	"bytes"
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

// TestMain gives the whole binary the two subkeys a running server always has:
// initEncryptionKey refuses to start without them, so a test suite without them
// would exercise a configuration that cannot exist.
//
// What it does *not* do is bind the sealing hooks. A test app starts with none,
// so a record saved through app.Save stays in the clear unless the test asked
// for them (setupFieldEncryption) — which is what keeps the fixtures readable
// while the writers that seal by hand, recordExecution and the two dependency
// columns, are exercised for real.
//
// The blind indexes are the exception, and setupBlindIndexes says why.
func TestMain(m *testing.M) {
	master := bytes.Repeat([]byte{0x2a}, 32)
	key, err := deriveKey(master, hkdfInfoCipher)
	if err != nil {
		panic(err)
	}
	cipherKey = key

	index, err := deriveKey(master, hkdfInfoIndex)
	if err != nil {
		panic(err)
	}
	indexKey = index

	os.Exit(m.Run())
}

// setupBlindIndexes stamps the fingerprints on a test app, whether or not the
// test asked for the sealing hooks.
//
// It is not optional the way sealing is, and that is the point: the unique index
// of a function name sits on nameHash, and resolution queries it. An app that
// skipped this would refuse its second fixture on a constraint and would resolve
// none of them by name — with the fixtures still perfectly readable, which is
// exactly what makes the symptom unreadable.
//
// The hooks carry an id, so calling it again on the same app replaces them
// rather than stacking a second pass.
func setupBlindIndexes(app core.App) {
	registerBlindIndexes(app)
}

// setupFieldEncryption binds on a test app the encryption hooks main.go binds,
// in the same order — the validation of a cron expression first, so it never
// weighs a sealed value, then the fingerprints, then the sealing.
func setupFieldEncryption(t testing.TB, app core.App) {
	t.Helper()
	bindTriggerHook(app)
	setupBlindIndexes(app)
	registerFieldEncryption(app)
}

// setupFaaSCollections creates the four FaaSBox collections on the test app, in
// the order OnServe uses: functions first, since the triggers and the logs
// carry a relation to it.
func setupFaaSCollections(t testing.TB, app core.App) {
	t.Helper()
	setupBlindIndexes(app)
	if err := ensureFunctionsCollection(app); err != nil {
		t.Fatalf("failed to create functions collection: %v", err)
	}
	if err := ensureAPIKeysCollection(app); err != nil {
		t.Fatalf("failed to create API keys collection: %v", err)
	}
	if err := ensureTriggersCollection(app); err != nil {
		t.Fatalf("failed to create triggers collection: %v", err)
	}
	if err := ensureLogsCollection(app); err != nil {
		t.Fatalf("failed to create logs collection: %v", err)
	}
}

// setupLogsCollection creates faasbox_logs together with the functions
// collection its relation points at, in the order OnServe uses.
func setupLogsCollection(t testing.TB, app core.App) {
	t.Helper()
	setupBlindIndexes(app)
	if err := ensureFunctionsCollection(app); err != nil {
		t.Fatalf("failed to create functions collection: %v", err)
	}
	if err := ensureLogsCollection(app); err != nil {
		t.Fatalf("failed to create logs collection: %v", err)
	}
}

// createTestAPIKey creates an invoke-only API key record and returns the raw key
// string.
func createTestAPIKey(t testing.TB, app core.App, name string, allowedFunctions []string) string {
	t.Helper()
	return createTestManageKey(t, app, name, allowedFunctions, false)
}

// createTestManageKey is createTestAPIKey with the canManage flag chosen, for
// the tests that exercise the function management routes.
func createTestManageKey(t testing.TB, app core.App, name string, allowedFunctions []string, canManage bool) string {
	t.Helper()
	rawKey, err := generateAPIKey(app, name, allowedFunctions, types.DateTime{}, canManage)
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

// defaultTestScript is the echo stub the fixtures fall back on.
const defaultTestScript = `const payload = await Bun.stdin.text(); console.log(JSON.stringify({echo: payload}));`

// setupTestFunctions saves one record per entry and mirrors each to disk, under
// its id, the way a save does. It is what any test going through the routes
// needs: they resolve the record before they ever build a path.
//
// funcs maps the function name to its index.ts (empty string = the echo stub).
// It returns the functions directory and the records, keyed by name.
func setupTestFunctions(t testing.TB, app core.App, funcs map[string]string) (string, map[string]*core.Record) {
	t.Helper()
	dir := t.TempDir()
	records := make(map[string]*core.Record, len(funcs))
	for name, content := range funcs {
		if content == "" {
			content = defaultTestScript
		}
		records[name] = saveTestFunction(t, app, dir, name, content, "")
	}
	return dir, records
}

// setupTestFunctionsDir creates a temp directory with function stubs, keyed by
// the directory name — which is the *id* on disk. It serves the engine-level
// tests, which call executeFunction directly and know nothing of the database.
// Anything going through a route wants setupTestFunctions instead.
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

// testFunctionId is the id the API scenarios point their triggers at. It has to
// be fixed: a scenario's request body is written before the app exists, so the
// relation it names cannot be a generated value.
const testFunctionId = "echofunction001"

// saveTestFunction stores a faasbox_functions record and mirrors it to disk,
// reproducing what the create/update hooks do before the dependency install is
// scheduled. An empty pkg means the function declares no dependencies.
func saveTestFunction(t testing.TB, app core.App, functionsDir, name, script, pkg string) *core.Record {
	t.Helper()
	return saveTestFunctionAs(t, app, functionsDir, "", name, script, pkg)
}

// saveTestFunctionAs is saveTestFunction with the record id chosen up front. An
// empty id lets PocketBase generate one, which is what every test wants except
// those whose fixture has to name the id in advance.
func saveTestFunctionAs(t testing.TB, app core.App, functionsDir, id, name, script, pkg string) *core.Record {
	t.Helper()
	setupBlindIndexes(app)
	if err := ensureFunctionsCollection(app); err != nil {
		t.Fatalf("failed to create functions collection: %v", err)
	}
	col, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
	if err != nil {
		t.Fatal(err)
	}

	// Looked up by fingerprint, which is what the server does: the column holds a
	// ciphertext on an app whose hooks seal, and this helper serves both kinds.
	record, err := app.FindFirstRecordByData(faasboxFunctionsCollection, "nameHash", blindIndex(name))
	if err != nil {
		record = core.NewRecord(col)
		if id != "" {
			record.Id = id
		}
		record.Set("name", name)
	}
	record.Set("script", script)
	record.Set("packageJson", pkg)
	if err := app.Save(record); err != nil {
		t.Fatalf("failed to save function %q: %v", name, err)
	}

	if err := syncRecordToDisk(app, record, functionsDir); err != nil {
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
//
// The name is compared after decryption rather than in the query: the column is
// encrypted at rest, and a nonce makes every writing of the same name a
// different value — no SQL predicate can match it.
func countExecutionLogs(t testing.TB, app core.App, functionName string) int {
	t.Helper()
	return len(executionLogsOf(t, app, functionName))
}

// executionLogsOf returns the faasbox_logs entries whose stored name is this one.
func executionLogsOf(t testing.TB, app core.App, functionName string) []*core.Record {
	t.Helper()
	records, err := app.FindAllRecords(faasboxLogsCollection)
	if err != nil {
		t.Fatalf("failed to read the execution logs of %q: %v", functionName, err)
	}

	matching := make([]*core.Record, 0, len(records))
	for _, record := range records {
		if decryptedText(app, record, "functionName") == functionName {
			matching = append(matching, record)
		}
	}
	return matching
}

// registerFaaSRoutes registers the FaaS HTTP routes on the test server's router,
// on an instance where FAASBOX_PUBLIC_URL is not set: no OAuth, so /mcp answers
// to an API key and to nothing else.
func registerFaaSRoutes(app *tests.TestApp, e *core.ServeEvent, functionsDir string) {
	registerFaaSRoutesFor(app, e, functionsDir, apiKeysOnly)
}

// registerFaaSRoutesFor is registerFaaSRoutes with the OAuth footing chosen, the
// way main.go hands it to the /mcp group alone.
func registerFaaSRoutesFor(app *tests.TestApp, e *core.ServeEvent, functionsDir string, oauth oauthConfig) {
	// Health check (public)
	e.Router.GET("/health", func(re *core.RequestEvent) error {
		return re.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Routes protected by API key
	faas := e.Router.Group("")
	faas.Bind(requireAPIKey(e.App, apiKeysOnly))
	faas.POST("/invoke/{name}", func(re *core.RequestEvent) error {
		return invokeHandler(re, functionsDir)
	})
	faas.GET("/functions", func(re *core.RequestEvent) error {
		return listFunctionsHandler(re, functionsDir)
	})

	// Function management (API key with canManage, or superuser)
	manage := e.Router.Group("/api/faasbox/functions")
	manage.Bind(requireAPIKey(e.App, apiKeysOnly))
	manage.Bind(requireManageKey())
	manage.POST("", createFunctionHandler)
	manage.GET("/{name}", getFunctionHandler)
	manage.PUT("/{name}", replaceFunctionHandler)
	manage.DELETE("/{name}", deleteFunctionHandler)
	manage.GET("/{name}/logs", functionLogsHandler)

	// The MCP server, mounted exactly as main.go mounts it — the middleware
	// chain is what the endpoint's refusals are made of.
	mcpRoutes := e.Router.Group("/mcp")
	mcpRoutes.Bind(requireAPIKey(e.App, oauth))
	mcpRoutes.Bind(requireManageKey())
	mcpRoutes.Bind(exposeKeyScope())
	mcpServe := apis.WrapStdHandler(mcpHandler(e.App, functionsDir))
	mcpRoutes.POST("", mcpServe)
	mcpRoutes.GET("", mcpServe)

	// Key management (superuser only)
	e.Router.POST("/api/faasbox/keys", func(re *core.RequestEvent) error {
		return createKeyHandler(re)
	}).Bind(apis.RequireSuperuserAuth())

	// Decrypted environment of a function (superuser only)
	e.Router.GET("/api/faasbox/functions/{name}/env", func(re *core.RequestEvent) error {
		return functionEnvHandler(re)
	}).Bind(apis.RequireSuperuserAuth())

	// Function directory browsing (superuser only)
	e.Router.GET("/api/faasbox/functions/{name}/files", func(re *core.RequestEvent) error {
		return functionFilesHandler(re, functionsDir)
	}).Bind(apis.RequireSuperuserAuth())
	e.Router.GET("/api/faasbox/functions/{name}/files/content", func(re *core.RequestEvent) error {
		return functionFileContentHandler(re, functionsDir)
	}).Bind(apis.RequireSuperuserAuth())
}

// setRecordDate overwrites a date column PocketBase manages itself (created) or
// that is only ever written by direct SQL (lastRunAt). A record.Set would not
// stick on an autodate field, which rewrites it at every save.
func setRecordDate(t testing.TB, app core.App, collection, recordId, column string, at time.Time) {
	t.Helper()
	_, err := app.DB().NewQuery(
		"UPDATE " + collection + " SET " + column + " = {:at} WHERE id = {:id}",
	).Bind(dbx.Params{
		"at": at.UTC().Format(types.DefaultDateLayout),
		"id": recordId,
	}).Execute()
	if err != nil {
		t.Fatalf("failed to set %s on %s: %v", column, recordId, err)
	}
}

// setCronJobDate is setRecordDate on faasbox_triggers.
func setCronJobDate(t testing.TB, app core.App, recordId, column string, at time.Time) {
	t.Helper()
	setRecordDate(t, app, faasboxTriggersCollection, recordId, column, at)
}

// createTestTrigger saves a trigger record pointing at a function id, and
// returns it.
func createTestTrigger(t testing.TB, app core.App, name, schedule, functionId string, active bool) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId(faasboxTriggersCollection)
	if err != nil {
		t.Fatal(err)
	}

	record := core.NewRecord(col)
	record.Set("name", name)
	record.Set("schedule", schedule)
	record.Set("function", functionId)
	record.Set("active", active)
	if err := app.Save(record); err != nil {
		t.Fatalf("failed to create cron record %q: %v", name, err)
	}
	return record
}

// assertNoStore holds the four responses that must never be written down: the
// three OAuth ones that carry a code or a token, and the instance mode, whose
// stale copy would let an editor promise what the server refuses. noStore
// carries the rule; this is what keeps a route from silently losing it.
func assertNoStore(t testing.TB, res *http.Response) {
	t.Helper()
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want %q", got, "no-store")
	}
	if got := res.Header.Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma = %q, want %q", got, "no-cache")
	}
}

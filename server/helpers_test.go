package main

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
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
	rawKey, err := generateAPIKey(app, name, allowedFunctions)
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
			content = `const input = await Bun.stdin.text(); console.log(JSON.stringify({echo: input}));`
		}
		if err := os.WriteFile(filepath.Join(funcDir, "index.ts"), []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write index.ts for %q: %v", name, err)
		}
	}
	return dir
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
}

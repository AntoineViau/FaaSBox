package main

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// --- Pure function tests ---

func TestValidName(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		wantOk bool
	}{
		// Valid names
		{"simple", "echo", true},
		{"with-hyphen", "my-func", true},
		{"single-char", "a", true},
		{"alphanumeric", "a1", true},
		{"complex-valid", "func-123-test", true},
		{"all-digits", "123", true},
		{"uppercase", "MyFunc", true},
		{"mixed-case-hyphen", "My-Func-1", true},

		// Invalid names
		{"empty", "", false},
		{"path-traversal", "../etc/passwd", false},
		{"dot-prefix", ".hidden", false},
		{"slash", "func/sub", false},
		{"space", "foo bar", false},
		{"starts-with-hyphen", "-start", false},
		{"ends-with-hyphen", "end-", false},
		{"at-sign", "foo@bar", false},
		{"semicolon", "foo;ls", false},
		{"backtick", "foo`id`", false},
		{"dollar", "foo$PATH", false},
		{"pipe", "foo|bar", false},
		{"ampersand", "foo&bar", false},
		{"null-byte", "foo\x00bar", false},
		{"backslash", "foo\\bar", false},
		{"double-dot", "foo..bar", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validName.MatchString(tc.input)
			if got != tc.wantOk {
				t.Errorf("validName.MatchString(%q) = %v, want %v", tc.input, got, tc.wantOk)
			}
		})
	}
}

func TestValidName_MaxLength(t *testing.T) {
	// A 64-char name should be accepted by regex (length check is separate)
	name64 := strings.Repeat("a", 64)
	if !validName.MatchString(name64) {
		t.Errorf("64-char name should match regex")
	}

	// The combined check as used in invokeHandler: regex + length
	name65 := strings.Repeat("a", 65)
	if validName.MatchString(name65) && len(name65) <= 64 {
		t.Error("65-char name should fail combined validation")
	}
	if !(validName.MatchString(name64) && len(name64) <= 64) {
		t.Error("64-char name should pass combined validation")
	}
}

// --- ApiScenario-based tests ---

func TestInvokeHandler_InvalidName(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	key := createTestAPIKey(t, app, "test", []string{"*"})

	scenarios := []tests.ApiScenario{
		{
			Name:   "name with special characters",
			Method: http.MethodPost,
			URL:    "/invoke/foo;ls",
			Headers: map[string]string{
				"X-API-Key": key,
			},
			TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
			DisableTestAppCleanup: true,
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				registerFaaSRoutes(app, e, t.TempDir())
			},
			ExpectedStatus:  400,
			ExpectedContent: []string{`invalid function name`},
		},
	}

	for _, s := range scenarios {
		s.Test(t)
	}
}

func TestInvokeHandler_FunctionNotFound(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	key := createTestAPIKey(t, app, "test-key", []string{"*"})
	functionsDir := setupTestFunctionsDir(t, map[string]string{}) // empty dir

	scenario := tests.ApiScenario{
		Name:   "nonexistent function returns 404",
		Method: http.MethodPost,
		URL:    "/invoke/nonexistent",
		Headers: map[string]string{
			"X-API-Key": key,
		},
		TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
		DisableTestAppCleanup: true,
		BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
			registerFaaSRoutes(app, e, functionsDir)
		},
		ExpectedStatus:  404,
		ExpectedContent: []string{`not found`},
	}
	scenario.Test(t)
}

func TestListFunctionsHandler(t *testing.T) {
	t.Run("lists functions with index.ts", func(t *testing.T) {
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()
		setupFaaSCollections(t, app)

		key := createTestAPIKey(t, app, "test", []string{"*"})
		functionsDir := setupTestFunctionsDir(t, map[string]string{
			"func-a": "",
			"func-b": "",
		})

		scenario := tests.ApiScenario{
			Name:   "two functions listed",
			Method: http.MethodGet,
			URL:    "/functions",
			Headers: map[string]string{
				"X-API-Key": key,
			},
			TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
			DisableTestAppCleanup: true,
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				registerFaaSRoutes(app, e, functionsDir)
			},
			ExpectedStatus:  200,
			ExpectedContent: []string{`"count":2`, `"func-a"`, `"func-b"`},
		}
		scenario.Test(t)
	})

	t.Run("empty directory", func(t *testing.T) {
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()
		setupFaaSCollections(t, app)

		key := createTestAPIKey(t, app, "test", []string{"*"})
		functionsDir := t.TempDir() // empty

		scenario := tests.ApiScenario{
			Name:   "empty directory returns count 0",
			Method: http.MethodGet,
			URL:    "/functions",
			Headers: map[string]string{
				"X-API-Key": key,
			},
			TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
			DisableTestAppCleanup: true,
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				registerFaaSRoutes(app, e, functionsDir)
			},
			ExpectedStatus:  200,
			ExpectedContent: []string{`"count":0`},
		}
		scenario.Test(t)
	})

	t.Run("skips directories without index.ts", func(t *testing.T) {
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()
		setupFaaSCollections(t, app)

		key := createTestAPIKey(t, app, "test", []string{"*"})
		functionsDir := setupTestFunctionsDir(t, map[string]string{
			"valid-func": "",
		})
		// Create a dir without index.ts
		if err := os.MkdirAll(functionsDir+"/no-index", 0o755); err != nil {
			t.Fatal(err)
		}

		scenario := tests.ApiScenario{
			Name:   "skips non-function directories",
			Method: http.MethodGet,
			URL:    "/functions",
			Headers: map[string]string{
				"X-API-Key": key,
			},
			TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
			DisableTestAppCleanup: true,
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				registerFaaSRoutes(app, e, functionsDir)
			},
			ExpectedStatus:     200,
			ExpectedContent:    []string{`"count":1`, `"valid-func"`},
			NotExpectedContent: []string{`"no-index"`},
		}
		scenario.Test(t)
	})
}

func TestHealthEndpoint(t *testing.T) {
	scenario := tests.ApiScenario{
		Name:   "health check returns ok without auth",
		Method: http.MethodGet,
		URL:    "/health",
		BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
			registerFaaSRoutes(app, e, t.TempDir())
		},
		ExpectedStatus:  200,
		ExpectedContent: []string{`"status":"ok"`},
	}
	scenario.Test(t)
}

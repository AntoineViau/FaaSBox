package main

import (
	"fmt"
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

// TestParseFunctionOutput covers truncation crossed with JSON validity. Only a
// truncated stdout that no longer parses is refused; every other combination
// must keep answering as it always did.
func TestParseFunctionOutput(t *testing.T) {
	cases := []struct {
		name       string
		stdout     string
		truncated  bool
		wantUsable bool
		wantResult any
	}{
		{"valid json, complete", `{"ok":true}`, false, true, map[string]any{"ok": true}},
		{"free text, complete", "hello world", false, true, "hello world"},
		{"valid json, truncated", `{"ok":true}`, true, true, map[string]any{"ok": true}},
		{"broken json, truncated", `{"ok":tr`, true, false, nil},
		{"free text, truncated", "hello wor", true, false, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, usable := parseFunctionOutput(tc.stdout, tc.truncated)
			if usable != tc.wantUsable {
				t.Fatalf("usable = %v, want %v", usable, tc.wantUsable)
			}
			if !usable {
				return
			}
			if got, want := fmt.Sprintf("%v", result), fmt.Sprintf("%v", tc.wantResult); got != want {
				t.Errorf("result = %s, want %s", got, want)
			}
		})
	}
}

// --- ApiScenario-based tests ---

// TestInvokeHandler_TruncatedOutputRejected locks the new refusal: a function
// whose JSON was cut at the capture cap must not have its fragment returned as
// a plain string, and the execution must be logged as an error.
func TestInvokeHandler_TruncatedOutputRejected(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	// Lower the capture cap so a small function can overflow it.
	original := maxOutputSize
	maxOutputSize = 64
	t.Cleanup(func() { maxOutputSize = original })

	key := createTestAPIKey(t, app, "test", []string{"*"})
	functionsDir, _ := setupTestFunctions(t, app, map[string]string{
		"big": `console.log(JSON.stringify({ big: "x".repeat(500) }));`,
	})

	scenario := tests.ApiScenario{
		Name:   "truncated json output is refused",
		Method: http.MethodPost,
		URL:    "/invoke/big",
		Headers: map[string]string{
			"X-API-Key": key,
		},
		TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
		DisableTestAppCleanup: true,
		BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
			registerFaaSRoutes(app, e, functionsDir)
		},
		ExpectedStatus: 502,
		ExpectedContent: []string{
			`64 bytes capture limit`,
			`FAASBOX_MAX_OUTPUT_SIZE`,
			`"truncated":true`,
		},
		NotExpectedContent: []string{`"result"`},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			record, err := app.FindFirstRecordByFilter(faasboxLogsCollection, "functionName = 'big'")
			if err != nil {
				t.Fatalf("no log recorded: %v", err)
			}
			if got := record.GetString("status"); got != "error" {
				t.Errorf("log status = %q, want %q", got, "error")
			}
		},
	}
	scenario.Test(t)
}

// TestInvokeHandler_FreeTextOutputAccepted is the counterpart: an output that
// fits under the cap and is not JSON still comes back as a raw string.
func TestInvokeHandler_FreeTextOutputAccepted(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	key := createTestAPIKey(t, app, "test", []string{"*"})
	functionsDir, _ := setupTestFunctions(t, app, map[string]string{
		"text": `console.log("plain answer");`,
	})

	scenario := tests.ApiScenario{
		Name:   "free text output stays a raw string",
		Method: http.MethodPost,
		URL:    "/invoke/text",
		Headers: map[string]string{
			"X-API-Key": key,
		},
		TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
		DisableTestAppCleanup: true,
		BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
			registerFaaSRoutes(app, e, functionsDir)
		},
		ExpectedStatus:     200,
		ExpectedContent:    []string{`"result":"plain answer`},
		NotExpectedContent: []string{`capture limit`},
	}
	scenario.Test(t)
}

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

// invokeFunction drives one HTTP invocation through the real route stack, so
// the dependency safety net, the execution log and the response all go through
// the code path an operator actually hits.
func invokeFunction(t *testing.T, app *tests.TestApp, functionsDir, name string, wantStatus int, wantContent []string) {
	t.Helper()
	key := createTestAPIKey(t, app, "deps-"+name, []string{"*"})
	scenario := tests.ApiScenario{
		Name:                  "invoke " + name,
		Method:                http.MethodPost,
		URL:                   "/invoke/" + name,
		Headers:               map[string]string{"X-API-Key": key},
		TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
		DisableTestAppCleanup: true,
		BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
			registerFaaSRoutes(app, e, functionsDir)
		},
		ExpectedStatus:  wantStatus,
		ExpectedContent: wantContent,
	}
	scenario.Test(t)
}

// TestInvokeHandler_DependencyFailureLeavesEveryTrace covers the blind spot this
// ticket closes: the HTTP path used to answer 500 and record nothing anywhere —
// no log line, no faasbox_logs entry, no state on the record. The cron path had
// always done both.
func TestInvokeHandler_DependencyFailureLeavesEveryTrace(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)
	enableServerLogs(t, app)

	functionsDir := t.TempDir()
	fakeBun(t, `echo "error: package nope@1.0.0 not found" >&2`+"\nexit 1")
	record := saveTestFunction(t, app, functionsDir, "broken-deps",
		"console.log('hi')", `{"dependencies":{"nope":"1.0.0"}}`)

	invokeFunction(t, app, functionsDir, "broken-deps", 500,
		[]string{"failed to install dependencies", "nope@1.0.0 not found"})

	// An invocation that failed to install did take place: it earns its line.
	if got := countExecutionLogs(t, app, "broken-deps"); got != 1 {
		t.Errorf("faasbox_logs holds %d entries for the invocation, want 1", got)
	}

	// And the server log, which is what an operator reads during an incident.
	messages := serverLogMessages(t, app)
	found := false
	for _, m := range messages {
		if strings.Contains(m, "faasbox http: execution failed") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("server log = %v, want a line reporting the failed invocation", messages)
	}

	// The state published by the safety net, which used to be written only by a
	// save — so a failure met on the invocation path left "pending" in place.
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

func TestInvokeHandler_SafetyNetPublishesReady(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	functionsDir := t.TempDir()
	fakeBun(t, "exit 0")
	record := saveTestFunction(t, app, functionsDir, "fresh-deps",
		"console.log('hi')", `{"dependencies":{"left-pad":"1.0.0"}}`)

	// A restart on a fresh filesystem restores the sources but not node_modules,
	// so "ready" survives in the database with nothing behind it. The first
	// invocation reinstalls and must say so.
	setDepsState(app, record.Id, "fresh-deps", depsStatusReady, "")

	invokeFunction(t, app, functionsDir, "fresh-deps", 200, []string{`"function":"fresh-deps"`})

	stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.GetString("depsStatus"); got != depsStatusReady {
		t.Errorf("depsStatus = %q, want %q after the safety net installed", got, depsStatusReady)
	}
	if got := stored.GetString("depsError"); got != "" {
		t.Errorf("depsError = %q, want it cleared by a successful install", got)
	}
}

// TestInvokeHandler_SafetyNetPersistsLockfile is the HTTP half of the parity: an
// install done by the safety net must pin its result on the record, exactly as the
// save path does. The cron path has the same test.
func TestInvokeHandler_SafetyNetPersistsLockfile(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	functionsDir := t.TempDir()
	fakeBun(t, "mkdir -p node_modules\necho resolved-by-http > bun.lock\nexit 0")
	record := saveTestFunction(t, app, functionsDir, "http-pins",
		"console.log('hi')", `{"dependencies":{"dayjs":"^1.11.0"}}`)

	invokeFunction(t, app, functionsDir, "http-pins", 200, []string{`"function":"http-pins"`})

	stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stored.GetString("bunLock")); got != "resolved-by-http" {
		t.Errorf("bunLock = %q, want what the safety net resolved", got)
	}
}

// TestInvokeHandler_HashCheckExitWritesNothing keeps the publication off the hot
// path: the overwhelming majority of invocations leave ensureDeps on the hash
// check, and a write per invocation would be pure cost.
func TestInvokeHandler_HashCheckExitWritesNothing(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	functionsDir := t.TempDir()
	fakeBun(t, "exit 0")
	record := saveTestFunction(t, app, functionsDir, "settled-deps",
		"console.log('hi')", `{"dependencies":{"left-pad":"1.0.0"}}`)

	// First invocation installs and publishes.
	invokeFunction(t, app, functionsDir, "settled-deps", 200, []string{`"function":"settled-deps"`})

	// A sentinel no code path would ever write: only an unwanted write shows up.
	setDepsState(app, record.Id, "settled-deps", depsStatusPending, "sentinel")

	invokeFunction(t, app, functionsDir, "settled-deps", 200, []string{`"function":"settled-deps"`})

	stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.GetString("depsStatus"); got != depsStatusPending {
		t.Errorf("depsStatus = %q, want the sentinel %q untouched", got, depsStatusPending)
	}
	if got := stored.GetString("depsError"); got != "sentinel" {
		t.Errorf("depsError = %q, want the sentinel untouched", got)
	}
}

// TestInvokeHandler_NotFoundStaysOutOfTheLogs is the one exclusion left standing:
// nothing ran, so there is no execution to record.
func TestInvokeHandler_NotFoundStaysOutOfTheLogs(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	functionsDir := setupTestFunctionsDir(t, map[string]string{})

	invokeFunction(t, app, functionsDir, "ghost", 404, []string{"not found"})

	if got := countExecutionLogs(t, app, "ghost"); got != 0 {
		t.Errorf("faasbox_logs holds %d entries for a function that does not exist, want 0", got)
	}
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
		functionsDir, _ := setupTestFunctions(t, app, map[string]string{
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

	// The inventory comes from the database, but the disk still says whether a
	// function can be invoked: a record whose script never reached it has no
	// index.ts, and listing it would advertise an invocation that answers 404.
	t.Run("skips records with no script on disk", func(t *testing.T) {
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()
		setupFaaSCollections(t, app)

		key := createTestAPIKey(t, app, "test", []string{"*"})
		functionsDir, _ := setupTestFunctions(t, app, map[string]string{
			"valid-func": "",
		})
		saveTestFunction(t, app, functionsDir, "no-index", "", "")
		// A stray directory answering to no record is invisible either way.
		if err := os.MkdirAll(functionsDir+"/orphan-dir-001", 0o755); err != nil {
			t.Fatal(err)
		}

		scenario := tests.ApiScenario{
			Name:   "skips records without an index.ts",
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
			NotExpectedContent: []string{`"no-index"`, `"orphan-dir-001"`},
		}
		scenario.Test(t)
	})

	// The id joins the response: it is what a scope lists and what survives a
	// rename, so a caller wiring an integration needs it.
	t.Run("each entry carries its id, name and invoke URL", func(t *testing.T) {
		app, err := tests.NewTestApp()
		if err != nil {
			t.Fatal(err)
		}
		defer app.Cleanup()
		setupFaaSCollections(t, app)

		key := createTestAPIKey(t, app, "test", []string{"*"})
		functionsDir, functions := setupTestFunctions(t, app, map[string]string{"echo": ""})

		scenario := tests.ApiScenario{
			Name:   "id, name and invoke URL",
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
			ExpectedStatus: 200,
			ExpectedContent: []string{
				`"id":"` + functions["echo"].Id + `"`,
				`"name":"echo"`,
				`"invoke":"/invoke/echo"`,
			},
		}
		scenario.Test(t)
	})
}

// TestListFunctionsHandler_Scope covers the enumeration side of the key scope.
// /functions carries no {name} path value, so the middleware cannot apply the
// restriction: without the filtering in the handler, any valid key learns the
// full inventory of the instance.
func TestListFunctionsHandler_Scope(t *testing.T) {
	// The scope is a list of ids now, so each case builds it from the records the
	// fixture created.
	cases := []struct {
		name               string
		scope              func(map[string]*core.Record) any
		expectedStatus     int
		expectedContent    []string
		notExpectedContent []string
	}{
		{
			name:               "scoped key sees only its function",
			scope:              func(f map[string]*core.Record) any { return []string{f["func-a"].Id} },
			expectedStatus:     200,
			expectedContent:    []string{`"count":1`, `"func-a"`},
			notExpectedContent: []string{`"func-b"`},
		},
		{
			name:            "wildcard sees everything",
			scope:           func(map[string]*core.Record) any { return []string{"*"} },
			expectedStatus:  200,
			expectedContent: []string{`"count":2`, `"func-a"`, `"func-b"`},
		},
		{
			name:            "empty list sees everything",
			scope:           func(map[string]*core.Record) any { return []string{} },
			expectedStatus:  200,
			expectedContent: []string{`"count":2`, `"func-a"`, `"func-b"`},
		},
		{
			name:            "scope naming an unknown id sees nothing",
			scope:           func(map[string]*core.Record) any { return []string{"doesnotexist01"} },
			expectedStatus:  200,
			expectedContent: []string{`"count":0`, `"functions":[]`},
		},
		{
			// A scope written in names no longer designates anything: the ids are
			// what the check compares, and a name is not one.
			name:            "scope written in names sees nothing",
			scope:           func(map[string]*core.Record) any { return []string{"func-a"} },
			expectedStatus:  200,
			expectedContent: []string{`"count":0`, `"functions":[]`},
		},
		{
			name:            "unreadable scope is denied",
			scope:           func(map[string]*core.Record) any { return `{"func-a":true}` },
			expectedStatus:  403,
			expectedContent: []string{`API key scope cannot be read`},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			app, err := tests.NewTestApp()
			if err != nil {
				t.Fatal(err)
			}
			defer app.Cleanup()
			setupFaaSCollections(t, app)

			key := createTestAPIKey(t, app, "scoped-listing", []string{"*"})

			functionsDir, functions := setupTestFunctions(t, app, map[string]string{
				"func-a": "",
				"func-b": "",
			})
			setKeyScope(t, app, key, tt.scope(functions))

			scenario := tests.ApiScenario{
				Name:   tt.name,
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
				ExpectedStatus:     tt.expectedStatus,
				ExpectedContent:    tt.expectedContent,
				NotExpectedContent: tt.notExpectedContent,
			}
			scenario.Test(t)
		})
	}
}

// TestListFunctionsHandler_Superuser locks the superuser path: it leaves no key
// record in the request context, and an absent record must read as "no scope",
// not as an empty allow-list.
func TestListFunctionsHandler_Superuser(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	functionsDir, _ := setupTestFunctions(t, app, map[string]string{
		"func-a": "",
		"func-b": "",
	})

	scenario := tests.ApiScenario{
		Name:   "superuser lists every function",
		Method: http.MethodGet,
		URL:    "/functions",
		Headers: map[string]string{
			"Authorization": superuserToken,
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
}

func TestScopeAllows(t *testing.T) {
	cases := []struct {
		name    string
		allowed []string
		target  string
		want    bool
	}{
		{"Nil scope allows any", nil, "echofunction001", true},
		{"Empty scope allows any", []string{}, "echofunction001", true},
		{"Wildcard allows any", []string{"*"}, "echofunction001", true},
		{"Listed id allowed", []string{"echofunction001", "pingfunction001"}, "pingfunction001", true},
		{"Unlisted id denied", []string{"echofunction001"}, "pingfunction001", false},
		{"Wildcard among ids allows any", []string{"echofunction001", "*"}, "pingfunction001", true},
		{"Match is exact", []string{"echofunction001"}, "echofunction0012", false},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := scopeAllows(tt.allowed, tt.target); got != tt.want {
				t.Errorf("scopeAllows(%v, %q) = %v, want %v", tt.allowed, tt.target, got, tt.want)
			}
		})
	}
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

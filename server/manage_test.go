package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// manageApp prepares an app with the four collections, the env hook main.go
// binds, and one function named "echo" carrying a lockfile and two secrets.
//
// The lockfile and the secrets are there for the same reason: they are what the
// contract must never publish, and what a replacement must never lose by
// accident.
func manageApp(t testing.TB) (*tests.TestApp, string, *core.Record) {
	t.Helper()
	withEncryptionKey(t)

	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)

	setupFaaSCollections(t, app)
	bindEnvHook(app)
	bindFunctionNameHook(app)

	functionsDir, functions := setupTestFunctions(t, app, map[string]string{"echo": ""})
	record := functions["echo"]
	record.Set("packageJson", `{"dependencies":{}}`)
	record.Set("bunLock", `{"lockfileVersion":1}`)
	record.Set("plainEnv", map[string]string{"TOKEN": "s3cr3t"})
	if err := app.Save(record); err != nil {
		t.Fatalf("failed to prepare the echo fixture: %v", err)
	}

	return app, functionsDir, record
}

// manageScenario wires a scenario onto an already prepared app.
func manageScenario(app *tests.TestApp, functionsDir string, s tests.ApiScenario) tests.ApiScenario {
	s.TestAppFactory = func(t testing.TB) *tests.TestApp { return app }
	s.DisableTestAppCleanup = true
	s.BeforeTestFunc = func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
		registerFaaSRoutes(app, e, functionsDir)
	}
	return s
}

// manageKeyHeader is the header a key with the management flag presents.
func manageKeyHeader(t testing.TB, app core.App, name string, scope []string) map[string]string {
	t.Helper()
	return map[string]string{"X-API-Key": createTestManageKey(t, app, name, scope, true)}
}

// secretsOf reads back the decrypted environment of a function.
func secretsOf(t testing.TB, app core.App, recordId string) map[string]string {
	t.Helper()
	record, err := app.FindRecordById(faasboxFunctionsCollection, recordId)
	if err != nil {
		t.Fatalf("function %q not found: %v", recordId, err)
	}
	env, err := decryptFunctionEnv(record)
	if err != nil {
		t.Fatalf("failed to decrypt the env of %q: %v", recordId, err)
	}
	return env
}

// cronsOf returns the trigger records of a function, keyed by name.
func cronsOf(t testing.TB, app core.App, functionId string) map[string]*core.Record {
	t.Helper()
	records, err := app.FindAllRecords(faasboxCronJobsCollection, dbx.HashExp{"function": functionId})
	if err != nil {
		t.Fatalf("failed to read the triggers of %q: %v", functionId, err)
	}
	byName := make(map[string]*core.Record, len(records))
	for _, record := range records {
		byName[record.GetString("name")] = record
	}
	return byName
}

// --- Create ---

func TestCreateFunctionHandler(t *testing.T) {
	t.Run("creates and answers 201 with the contract", func(t *testing.T) {
		app, dir, _ := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:    "create",
			Method:  http.MethodPost,
			URL:     "/api/faasbox/functions",
			Body:    strings.NewReader(`{"name":"greet","script":"console.log('{}')","packageJson":"{}"}`),
			Headers: manageKeyHeader(t, app, "manager", nil),
			// The contract, and nothing of the mechanics behind it.
			ExpectedStatus: 201,
			ExpectedContent: []string{
				`"name":"greet"`,
				`"packageJson":"{}"`,
				`"depsStatus":""`,
				`"crons":[]`,
				`"id":"`,
			},
			NotExpectedContent: []string{`"env"`, `"bunLock"`, `"plainEnv"`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if _, err := app.FindFirstRecordByData(faasboxFunctionsCollection, "name", "greet"); err != nil {
					t.Fatalf("the function was not persisted: %v", err)
				}
			},
		})
		s.Test(t)
	})

	t.Run("creates the triggers carried by the body", func(t *testing.T) {
		app, dir, _ := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:   "create with crons",
			Method: http.MethodPost,
			URL:    "/api/faasbox/functions",
			Body: strings.NewReader(`{"name":"nightly-job","script":"console.log('{}')",
				"crons":[{"name":"nightly","schedule":"0 3 * * *","payload":{"a":1},"maxQueue":2}]}`),
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  201,
			ExpectedContent: []string{`"name":"nightly"`, `"schedule":"0 3 * * *"`, `"maxQueue":2`, `"active":true`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				fn, err := app.FindFirstRecordByData(faasboxFunctionsCollection, "name", "nightly-job")
				if err != nil {
					t.Fatalf("the function was not persisted: %v", err)
				}
				crons := cronsOf(t, app, fn.Id)
				if len(crons) != 1 {
					t.Fatalf("got %d triggers, want 1", len(crons))
				}
				// An omitted "active" means the trigger fires: declaring one is
				// asking for it, and the zero value would answer the opposite.
				if !crons["nightly"].GetBool("active") {
					t.Error("the trigger was created inactive")
				}
				if got := crons["nightly"].GetString("function"); got != fn.Id {
					t.Errorf("trigger relation = %q, want the function id %q", got, fn.Id)
				}
			},
		})
		s.Test(t)
	})

	t.Run("conflicts on a name already taken", func(t *testing.T) {
		app, dir, _ := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "duplicate name",
			Method:          http.MethodPost,
			URL:             "/api/faasbox/functions",
			Body:            strings.NewReader(`{"name":"echo","script":"console.log('{}')"}`),
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  409,
			ExpectedContent: []string{`already carries this name`},
		})
		s.Test(t)
	})

	t.Run("refuses a name validName rejects", func(t *testing.T) {
		app, dir, _ := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:           "invalid name",
			Method:         http.MethodPost,
			URL:            "/api/faasbox/functions",
			Body:           strings.NewReader(`{"name":"../escape","script":"console.log('{}')"}`),
			Headers:        manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus: 400,
			// The refusal reaches this route through the hook, so what is checked
			// here is that classifyManageFailure hands the wording back intact.
			ExpectedContent: []string{
				`Invalid function name \"../escape\"`,
				`letters, digits and hyphens only`,
			},
		})
		s.Test(t)
	})

	t.Run("refuses a body that is not JSON", func(t *testing.T) {
		app, dir, _ := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "invalid JSON",
			Method:          http.MethodPost,
			URL:             "/api/faasbox/functions",
			Body:            strings.NewReader(`not json`),
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  400,
			ExpectedContent: []string{`Invalid JSON body`},
		})
		s.Test(t)
	})

	t.Run("refuses a key whose scope names precise functions", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "restricted scope cannot create",
			Method:          http.MethodPost,
			URL:             "/api/faasbox/functions",
			Body:            strings.NewReader(`{"name":"greet","script":"console.log('{}')"}`),
			Headers:         manageKeyHeader(t, app, "scoped", []string{fn.Id}),
			ExpectedStatus:  403,
			ExpectedContent: []string{`restricted scope cannot create`},
		})
		s.Test(t)
	})

	t.Run("lets a superuser through without a key", func(t *testing.T) {
		app, dir, _ := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "superuser create",
			Method:          http.MethodPost,
			URL:             "/api/faasbox/functions",
			Body:            strings.NewReader(`{"name":"greet","script":"console.log('{}')"}`),
			Headers:         map[string]string{"Authorization": superuserToken},
			ExpectedStatus:  201,
			ExpectedContent: []string{`"name":"greet"`},
		})
		s.Test(t)
	})
}

// --- The two guards, on all four routes ---

func TestManageRoutesGuards(t *testing.T) {
	routes := []struct {
		method string
		url    string
		body   string
	}{
		{http.MethodPost, "/api/faasbox/functions", `{"name":"greet"}`},
		{http.MethodGet, "/api/faasbox/functions/echo", ""},
		{http.MethodPut, "/api/faasbox/functions/echo", `{}`},
		{http.MethodDelete, "/api/faasbox/functions/echo", ""},
	}

	for _, route := range routes {
		t.Run(route.method+" without an API key", func(t *testing.T) {
			app, dir, _ := manageApp(t)
			s := manageScenario(app, dir, tests.ApiScenario{
				Name:            "no header",
				Method:          route.method,
				URL:             route.url,
				Body:            strings.NewReader(route.body),
				ExpectedStatus:  401,
				ExpectedContent: []string{`Missing X-API-Key header`},
			})
			s.Test(t)
		})

		t.Run(route.method+" with an invoke-only key", func(t *testing.T) {
			app, dir, _ := manageApp(t)
			s := manageScenario(app, dir, tests.ApiScenario{
				Name:            "no canManage",
				Method:          route.method,
				URL:             route.url,
				Body:            strings.NewReader(route.body),
				Headers:         map[string]string{"X-API-Key": createTestAPIKey(t, app, "invoke-only", nil)},
				ExpectedStatus:  403,
				ExpectedContent: []string{`not authorized to manage functions`},
			})
			s.Test(t)
		})
	}
}

// --- Read ---

func TestGetFunctionHandler(t *testing.T) {
	t.Run("answers 200 with the contract and nothing else", func(t *testing.T) {
		app, dir, _ := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "read",
			Method:          http.MethodGet,
			URL:             "/api/faasbox/functions/echo",
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  200,
			ExpectedContent: []string{`"name":"echo"`, `"packageJson":"{\"dependencies\":{}}"`, `"crons":[]`},
			// The encrypted env and the lockfile are on the record and must not
			// reach the caller: they are mechanics, not contract.
			NotExpectedContent: []string{`"env"`, `"bunLock"`, `lockfileVersion`},
		})
		s.Test(t)
	})

	t.Run("reads by id as well as by name", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "read by id",
			Method:          http.MethodGet,
			URL:             "/api/faasbox/functions/" + fn.Id,
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  200,
			ExpectedContent: []string{`"name":"echo"`},
		})
		s.Test(t)
	})

	t.Run("answers 404 for a segment designating nothing", func(t *testing.T) {
		app, dir, _ := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "unknown function",
			Method:          http.MethodGet,
			URL:             "/api/faasbox/functions/nope",
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  404,
			ExpectedContent: []string{`Function not found`},
		})
		s.Test(t)
	})

	t.Run("lets a superuser through without a key", func(t *testing.T) {
		app, dir, _ := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "superuser read",
			Method:          http.MethodGet,
			URL:             "/api/faasbox/functions/echo",
			Headers:         map[string]string{"Authorization": superuserToken},
			ExpectedStatus:  200,
			ExpectedContent: []string{`"name":"echo"`},
		})
		s.Test(t)
	})

	t.Run("a scoped key reaches the function it names", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "scoped read allowed",
			Method:          http.MethodGet,
			URL:             "/api/faasbox/functions/echo",
			Headers:         manageKeyHeader(t, app, "scoped", []string{fn.Id}),
			ExpectedStatus:  200,
			ExpectedContent: []string{`"name":"echo"`},
		})
		s.Test(t)
	})

	t.Run("a scoped key is refused elsewhere", func(t *testing.T) {
		app, dir, _ := manageApp(t)
		other := saveTestFunction(t, app, dir, "other-func", defaultTestScript, "")
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "scoped read refused",
			Method:          http.MethodGet,
			URL:             "/api/faasbox/functions/echo",
			Headers:         manageKeyHeader(t, app, "scoped-elsewhere", []string{other.Id}),
			ExpectedStatus:  403,
			ExpectedContent: []string{`API key is not authorized for function`},
		})
		s.Test(t)
	})
}

// --- Replace ---

func TestReplaceFunctionHandler(t *testing.T) {
	t.Run("replaces the script and the package.json", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "replace",
			Method:          http.MethodPut,
			URL:             "/api/faasbox/functions/echo",
			Body:            strings.NewReader(`{"script":"console.log('replaced')","packageJson":"{\"name\":\"x\"}"}`),
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  200,
			ExpectedContent: []string{`console.log('replaced')`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				record, err := app.FindRecordById(faasboxFunctionsCollection, fn.Id)
				if err != nil {
					t.Fatal(err)
				}
				if got := record.GetString("script"); got != `console.log('replaced')` {
					t.Errorf("script = %q, want the replacement", got)
				}
				if got := record.GetString("packageJson"); got != `{"name":"x"}` {
					t.Errorf("packageJson = %q, want the replacement", got)
				}
			},
		})
		s.Test(t)
	})

	t.Run("answers 404 and creates nothing", func(t *testing.T) {
		app, dir, _ := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "replace unknown",
			Method:          http.MethodPut,
			URL:             "/api/faasbox/functions/nope",
			Body:            strings.NewReader(`{"script":"console.log('{}')"}`),
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  404,
			ExpectedContent: []string{`Function not found`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if _, err := app.FindFirstRecordByData(faasboxFunctionsCollection, "name", "nope"); err == nil {
					t.Error("PUT created a function instead of refusing")
				}
			},
		})
		s.Test(t)
	})

	t.Run("refuses a name that designates another function", func(t *testing.T) {
		app, dir, _ := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "rename attempt",
			Method:          http.MethodPut,
			URL:             "/api/faasbox/functions/echo",
			Body:            strings.NewReader(`{"name":"echo-v2","script":"console.log('{}')"}`),
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  400,
			ExpectedContent: []string{`never renames`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if _, err := app.FindFirstRecordByData(faasboxFunctionsCollection, "name", "echo"); err != nil {
					t.Error("the function was renamed despite the refusal")
				}
			},
		})
		s.Test(t)
	})

	t.Run("accepts the id as the name in the body", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "round trip by id",
			Method:          http.MethodPut,
			URL:             "/api/faasbox/functions/" + fn.Id,
			Body:            strings.NewReader(`{"name":"` + fn.Id + `","script":"console.log('{}')"}`),
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  200,
			ExpectedContent: []string{`"name":"echo"`},
		})
		s.Test(t)
	})

	t.Run("leaves the secrets alone when the body says nothing", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "secrets preserved",
			Method:          http.MethodPut,
			URL:             "/api/faasbox/functions/echo",
			Body:            strings.NewReader(`{"script":"console.log('{}')","packageJson":"{\"dependencies\":{}}"}`),
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  200,
			ExpectedContent: []string{`"name":"echo"`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if got := secretsOf(t, app, fn.Id); got["TOKEN"] != "s3cr3t" {
					t.Errorf("secrets = %v, want TOKEN preserved", got)
				}
			},
		})
		s.Test(t)
	})

	t.Run("erases every secret on an empty plainEnv", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:               "secrets erased",
			Method:             http.MethodPut,
			URL:                "/api/faasbox/functions/echo",
			Body:               strings.NewReader(`{"script":"console.log('{}')","plainEnv":{}}`),
			Headers:            manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:     200,
			ExpectedContent:    []string{`"name":"echo"`},
			NotExpectedContent: []string{`plainEnv`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if got := secretsOf(t, app, fn.Id); len(got) != 0 {
					t.Errorf("secrets = %v, want none left", got)
				}
			},
		})
		s.Test(t)
	})

	t.Run("replaces every secret on a non-empty plainEnv", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:               "secrets replaced",
			Method:             http.MethodPut,
			URL:                "/api/faasbox/functions/echo",
			Body:               strings.NewReader(`{"script":"console.log('{}')","plainEnv":{"OTHER":"v"}}`),
			Headers:            manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:     200,
			ExpectedContent:    []string{`"name":"echo"`},
			NotExpectedContent: []string{`plainEnv`, `OTHER`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				got := secretsOf(t, app, fn.Id)
				if len(got) != 1 || got["OTHER"] != "v" {
					t.Errorf("secrets = %v, want only OTHER", got)
				}
			},
		})
		s.Test(t)
	})

	t.Run("stores a secret far past the old default cap", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		// 50 KB of plaintext is roughly 67 KB once encrypted and base64-encoded,
		// well past the 5000 runes an undeclared field allowed and well inside
		// maxEnvSize. A single long private key used to be enough to be refused.
		secret := strings.Repeat("k", 50_000)
		body, err := json.Marshal(map[string]any{
			"script":   "console.log('{}')",
			"plainEnv": map[string]string{"BIG_KEY": secret},
		})
		if err != nil {
			t.Fatal(err)
		}
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:               "large secret",
			Method:             http.MethodPut,
			URL:                "/api/faasbox/functions/echo",
			Body:               strings.NewReader(string(body)),
			Headers:            manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:     200,
			ExpectedContent:    []string{`"name":"echo"`},
			NotExpectedContent: []string{`BIG_KEY`, `"env"`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if got := secretsOf(t, app, fn.Id)["BIG_KEY"]; got != secret {
					t.Errorf("the stored secret is %d characters, want %d", len(got), len(secret))
				}
			},
		})
		s.Test(t)
	})

	t.Run("refuses a secret past the cap, and says plainEnv rather than env", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		// Base64 costs a third, so this lands past maxEnvSize once encrypted.
		body, err := json.Marshal(map[string]any{
			"script":   "console.log('{}')",
			"plainEnv": map[string]string{"HUGE": strings.Repeat("k", 90_000)},
		})
		if err != nil {
			t.Fatal(err)
		}
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:           "oversized secret",
			Method:         http.MethodPut,
			URL:            "/api/faasbox/functions/echo",
			Body:           strings.NewReader(string(body)),
			Headers:        manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus: 400,
			// The caller sent plainEnv and never heard of env, which is exactly
			// the column this contract hides.
			ExpectedContent:    []string{`"plainEnv"`},
			NotExpectedContent: []string{`"env"`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if got := secretsOf(t, app, fn.Id); got["TOKEN"] != "s3cr3t" {
					t.Errorf("secrets = %v, want the refusal to have changed nothing", got)
				}
			},
		})
		s.Test(t)
	})

	t.Run("refuses a plainEnv that is not an object of strings", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "malformed plainEnv",
			Method:          http.MethodPut,
			URL:             "/api/faasbox/functions/echo",
			Body:            strings.NewReader(`{"script":"console.log('{}')","plainEnv":["A"]}`),
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  400,
			ExpectedContent: []string{`plainEnv`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if got := secretsOf(t, app, fn.Id); got["TOKEN"] != "s3cr3t" {
					t.Errorf("secrets = %v, want TOKEN untouched by the refusal", got)
				}
			},
		})
		s.Test(t)
	})

	t.Run("a scoped key replaces the function it names, and no other", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		other := saveTestFunction(t, app, dir, "other-func", defaultTestScript, "")
		key := manageKeyHeader(t, app, "scoped", []string{fn.Id})

		allowed := manageScenario(app, dir, tests.ApiScenario{
			Name:            "scoped replace allowed",
			Method:          http.MethodPut,
			URL:             "/api/faasbox/functions/echo",
			Body:            strings.NewReader(`{"script":"console.log('scoped')"}`),
			Headers:         key,
			ExpectedStatus:  200,
			ExpectedContent: []string{`console.log('scoped')`},
		})
		allowed.Test(t)

		refused := manageScenario(app, dir, tests.ApiScenario{
			Name:            "scoped replace refused",
			Method:          http.MethodPut,
			URL:             "/api/faasbox/functions/" + other.Id,
			Body:            strings.NewReader(`{"script":"console.log('nope')"}`),
			Headers:         key,
			ExpectedStatus:  403,
			ExpectedContent: []string{`API key is not authorized for function`},
		})
		refused.Test(t)
	})

	t.Run("accepts a script far past the old default cap", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		// The undeclared-Max default was 5000 runes, short enough to turn away an
		// ordinary function. This is what the raise is for.
		script := "// " + strings.Repeat("x", 200_000)
		body, err := json.Marshal(map[string]string{"script": script})
		if err != nil {
			t.Fatal(err)
		}
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "large script",
			Method:          http.MethodPut,
			URL:             "/api/faasbox/functions/echo",
			Body:            strings.NewReader(string(body)),
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  200,
			ExpectedContent: []string{`"name":"echo"`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				record, err := app.FindRecordById(faasboxFunctionsCollection, fn.Id)
				if err != nil {
					t.Fatal(err)
				}
				if got := len(record.GetString("script")); got != len(script) {
					t.Errorf("stored script is %d characters, want %d", got, len(script))
				}
			},
		})
		s.Test(t)
	})

	t.Run("names the field when the record is refused", func(t *testing.T) {
		app, dir, _ := manageApp(t)
		// Past maxSourceSize the record is refused, and that refusal belongs to
		// the caller's body rather than to the server: it answers 400 and says
		// which field it is about.
		oversized, err := json.Marshal(map[string]string{"script": strings.Repeat("x", maxSourceSize+1)})
		if err != nil {
			t.Fatal(err)
		}
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "oversized script",
			Method:          http.MethodPut,
			URL:             "/api/faasbox/functions/echo",
			Body:            strings.NewReader(string(oversized)),
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  400,
			ExpectedContent: []string{`"fields"`, `script`},
		})
		s.Test(t)
	})

	t.Run("leaves bunLock alone", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:               "lockfile preserved",
			Method:             http.MethodPut,
			URL:                "/api/faasbox/functions/echo",
			Body:               strings.NewReader(`{"script":"console.log('{}')","packageJson":"{\"dependencies\":{}}"}`),
			Headers:            manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:     200,
			ExpectedContent:    []string{`"name":"echo"`},
			NotExpectedContent: []string{`bunLock`, `lockfileVersion`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				record, err := app.FindRecordById(faasboxFunctionsCollection, fn.Id)
				if err != nil {
					t.Fatal(err)
				}
				if got := record.GetString("bunLock"); got != `{"lockfileVersion":1}` {
					t.Errorf("bunLock = %q, want it untouched", got)
				}
			},
		})
		s.Test(t)
	})
}

// --- Replace: the triggers ---

func TestReplaceFunctionHandler_Crons(t *testing.T) {
	t.Run("an absent crons key leaves the triggers in place", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		createTestCronJob(t, app, "nightly", "0 3 * * *", fn.Id, true)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "crons preserved",
			Method:          http.MethodPut,
			URL:             "/api/faasbox/functions/echo",
			Body:            strings.NewReader(`{"script":"console.log('{}')"}`),
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  200,
			ExpectedContent: []string{`"name":"nightly"`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if got := cronsOf(t, app, fn.Id); len(got) != 1 {
					t.Errorf("got %d triggers, want the existing one preserved", len(got))
				}
			},
		})
		s.Test(t)
	})

	t.Run("an empty list removes every trigger", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		createTestCronJob(t, app, "nightly", "0 3 * * *", fn.Id, true)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "crons cleared",
			Method:          http.MethodPut,
			URL:             "/api/faasbox/functions/echo",
			Body:            strings.NewReader(`{"script":"console.log('{}')","crons":[]}`),
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  200,
			ExpectedContent: []string{`"crons":[]`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if got := cronsOf(t, app, fn.Id); len(got) != 0 {
					t.Errorf("got %d triggers, want none left", len(got))
				}
			},
		})
		s.Test(t)
	})

	t.Run("a non-empty list replaces the whole set", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		createTestCronJob(t, app, "nightly", "0 3 * * *", fn.Id, true)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:   "crons replaced",
			Method: http.MethodPut,
			URL:    "/api/faasbox/functions/echo",
			Body: strings.NewReader(`{"script":"console.log('{}')",
				"crons":[{"name":"hourly","schedule":"0 * * * *","active":false,"maxQueue":3}]}`),
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  200,
			ExpectedContent: []string{`"name":"hourly"`, `"active":false`, `"maxQueue":3`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				crons := cronsOf(t, app, fn.Id)
				if len(crons) != 1 || crons["hourly"] == nil {
					t.Fatalf("triggers = %v, want only hourly", crons)
				}
				if crons["hourly"].GetBool("active") {
					t.Error("active:false was not honoured")
				}
			},
		})
		s.Test(t)
	})

	t.Run("a refused schedule rolls the whole write back", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		// The validation hook main.go binds is what refuses the expression; the
		// transaction is what keeps the refusal from being destructive — and it
		// has to cover the function record too, not only the triggers.
		app.OnRecordCreate(faasboxCronJobsCollection).BindFunc(validateCronScheduleHook)
		createTestCronJob(t, app, "nightly", "0 3 * * *", fn.Id, true)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:   "invalid schedule rolls back",
			Method: http.MethodPut,
			URL:    "/api/faasbox/functions/echo",
			Body: strings.NewReader(`{"script":"console.log('replaced')","plainEnv":{},
				"crons":[{"name":"broken","schedule":"not a cron"}]}`),
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  400,
			ExpectedContent: []string{`Invalid cron expression`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				crons := cronsOf(t, app, fn.Id)
				if len(crons) != 1 || crons["nightly"] == nil {
					t.Fatalf("triggers = %v, want the existing one still there", crons)
				}
				record, err := app.FindRecordById(faasboxFunctionsCollection, fn.Id)
				if err != nil {
					t.Fatal(err)
				}
				if got := record.GetString("script"); got != defaultTestScript {
					t.Errorf("script = %q, want the refusal to have rolled it back", got)
				}
				// The destructive half: "plainEnv": {} erases every secret, and it
				// must not survive a request the caller was told had failed.
				if got := secretsOf(t, app, fn.Id); got["TOKEN"] != "s3cr3t" {
					t.Errorf("secrets = %v, want TOKEN rolled back with the rest", got)
				}
			},
		})
		s.Test(t)
	})

	t.Run("a refused schedule creates no function at all", func(t *testing.T) {
		app, dir, _ := manageApp(t)
		app.OnRecordCreate(faasboxCronJobsCollection).BindFunc(validateCronScheduleHook)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:   "create rolls back",
			Method: http.MethodPost,
			URL:    "/api/faasbox/functions",
			Body: strings.NewReader(`{"name":"greet","script":"console.log('{}')",
				"crons":[{"name":"broken","schedule":"not a cron"}]}`),
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  400,
			ExpectedContent: []string{`Invalid cron expression`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				// Left behind, the function would turn the corrected retry into a
				// 409 — the caller reads 400 and cannot recover.
				if _, err := app.FindFirstRecordByData(faasboxFunctionsCollection, "name", "greet"); err == nil {
					t.Error("the refused create left a function behind")
				}
			},
		})
		s.Test(t)
	})

	t.Run("a rolled back create fires no after-success hook", func(t *testing.T) {
		app, dir, _ := manageApp(t)
		app.OnRecordCreate(faasboxCronJobsCollection).BindFunc(validateCronScheduleHook)

		// syncRecordToDisk and scheduleDepsInstall both hang off this hook. If it
		// fired for a record the transaction rolled back, a refused POST would
		// leave a directory on disk and schedule an install for a function that
		// does not exist. PocketBase defers the hook to the end of the
		// transaction and triggers the *Error* variant when it failed — this
		// pins the consequence rather than the mechanism.
		fired := false
		app.OnRecordAfterCreateSuccess(faasboxFunctionsCollection).BindFunc(func(e *core.RecordEvent) error {
			fired = true
			return e.Next()
		})

		s := manageScenario(app, dir, tests.ApiScenario{
			Name:   "no after-success on rollback",
			Method: http.MethodPost,
			URL:    "/api/faasbox/functions",
			Body: strings.NewReader(`{"name":"greet","script":"console.log('{}')",
				"crons":[{"name":"broken","schedule":"not a cron"}]}`),
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  400,
			ExpectedContent: []string{`Invalid cron expression`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if fired {
					t.Error("the after-success hook ran for a record the transaction rolled back")
				}
			},
		})
		s.Test(t)
	})

	t.Run("names the trigger when one of its fields is refused", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		// No schedule at all: validateCronScheduleHook only looks at a non-empty
		// one, so this falls through to the Required field validation. That is
		// the caller's data like any other, and it answers 400, not 500.
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "trigger missing its schedule",
			Method:          http.MethodPut,
			URL:             "/api/faasbox/functions/echo",
			Body:            strings.NewReader(`{"script":"console.log('{}')","crons":[{"name":"no-schedule"}]}`),
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  400,
			ExpectedContent: []string{`Trigger \"no-schedule\" was refused`, `"fields"`, `schedule`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if got := cronsOf(t, app, fn.Id); len(got) != 0 {
					t.Errorf("triggers = %v, want none written", got)
				}
				record, err := app.FindRecordById(faasboxFunctionsCollection, fn.Id)
				if err != nil {
					t.Fatal(err)
				}
				if got := record.GetString("script"); got != defaultTestScript {
					t.Errorf("script = %q, want the refusal to have rolled it back", got)
				}
			},
		})
		s.Test(t)
	})

	t.Run("falls back to the position when the name is what is missing", func(t *testing.T) {
		app, dir, _ := manageApp(t)
		// `Trigger ""` names nothing, and a body may carry several triggers.
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:   "trigger missing its name",
			Method: http.MethodPut,
			URL:    "/api/faasbox/functions/echo",
			Body: strings.NewReader(`{"script":"console.log('{}')","crons":[
				{"name":"first","schedule":"0 3 * * *"},
				{"schedule":"0 4 * * *"}]}`),
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  400,
			ExpectedContent: []string{`Trigger #2 was refused`, `name`},
		})
		s.Test(t)
	})
}

// --- Delete ---

func TestDeleteFunctionHandler(t *testing.T) {
	t.Run("deletes and answers 204, triggers included", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		createTestCronJob(t, app, "nightly", "0 3 * * *", fn.Id, true)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:           "delete",
			Method:         http.MethodDelete,
			URL:            "/api/faasbox/functions/echo",
			Headers:        manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus: 204,
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if _, err := app.FindRecordById(faasboxFunctionsCollection, fn.Id); err == nil {
					t.Error("the function record survived the delete")
				}
				// No cleanup code of ours: the relation carries CascadeDelete.
				if got := cronsOf(t, app, fn.Id); len(got) != 0 {
					t.Errorf("got %d triggers, want them gone with the function", len(got))
				}
			},
		})
		s.Test(t)
	})

	t.Run("a scoped key deletes the function it names", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:           "scoped delete allowed",
			Method:         http.MethodDelete,
			URL:            "/api/faasbox/functions/echo",
			Headers:        manageKeyHeader(t, app, "scoped", []string{fn.Id}),
			ExpectedStatus: 204,
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if _, err := app.FindRecordById(faasboxFunctionsCollection, fn.Id); err == nil {
					t.Error("the function record survived the delete")
				}
			},
		})
		s.Test(t)
	})

	t.Run("answers 404 for a segment designating nothing", func(t *testing.T) {
		app, dir, _ := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "delete unknown",
			Method:          http.MethodDelete,
			URL:             "/api/faasbox/functions/nope",
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  404,
			ExpectedContent: []string{`Function not found`},
		})
		s.Test(t)
	})

	t.Run("a scoped key deletes what it names and nothing else", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		other := saveTestFunction(t, app, dir, "other-func", defaultTestScript, "")
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:            "scoped delete refused",
			Method:          http.MethodDelete,
			URL:             "/api/faasbox/functions/other-func",
			Headers:         manageKeyHeader(t, app, "scoped", []string{fn.Id}),
			ExpectedStatus:  403,
			ExpectedContent: []string{`API key is not authorized for function`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if _, err := app.FindRecordById(faasboxFunctionsCollection, other.Id); err != nil {
					t.Error("the refused delete went through anyway")
				}
			},
		})
		s.Test(t)
	})

	t.Run("lets a superuser through without a key", func(t *testing.T) {
		app, dir, fn := manageApp(t)
		s := manageScenario(app, dir, tests.ApiScenario{
			Name:           "superuser delete",
			Method:         http.MethodDelete,
			URL:            "/api/faasbox/functions/echo",
			Headers:        map[string]string{"Authorization": superuserToken},
			ExpectedStatus: 204,
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if _, err := app.FindRecordById(faasboxFunctionsCollection, fn.Id); err == nil {
					t.Error("the function record survived the delete")
				}
			},
		})
		s.Test(t)
	})
}

// --- The key flag itself ---

func TestCreateKeyHandler_CanManage(t *testing.T) {
	scenarios := []tests.ApiScenario{
		{
			Name:    "flag written when the body carries it",
			Method:  http.MethodPost,
			URL:     "/api/faasbox/keys",
			Body:    strings.NewReader(`{"name":"managing","canManage":true}`),
			Headers: map[string]string{"Authorization": superuserToken},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFaaSRoutes(app, e, t.TempDir())
			},
			ExpectedStatus:  200,
			ExpectedContent: []string{`"key":"fbx_`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				record, err := app.FindFirstRecordByData(faasboxAPIKeysCollection, "name", "managing")
				if err != nil {
					t.Fatalf("key record not found: %v", err)
				}
				if !record.GetBool("canManage") {
					t.Error("canManage was not persisted")
				}
			},
		},
		{
			Name:    "absent flag means invoke-only",
			Method:  http.MethodPost,
			URL:     "/api/faasbox/keys",
			Body:    strings.NewReader(`{"name":"plain"}`),
			Headers: map[string]string{"Authorization": superuserToken},
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				setupFaaSCollections(t, app)
				registerFaaSRoutes(app, e, t.TempDir())
			},
			ExpectedStatus:  200,
			ExpectedContent: []string{`"name":"plain"`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				record, err := app.FindFirstRecordByData(faasboxAPIKeysCollection, "name", "plain")
				if err != nil {
					t.Fatalf("key record not found: %v", err)
				}
				if record.GetBool("canManage") {
					t.Error("an absent canManage produced a management key")
				}
			},
		},
	}

	for _, s := range scenarios {
		s.Test(t)
	}
}

func TestEnsureAPIKeysCollection_CanManageField(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	if err := ensureAPIKeysCollection(app); err != nil {
		t.Fatal(err)
	}
	col, err := app.FindCollectionByNameOrId(faasboxAPIKeysCollection)
	if err != nil {
		t.Fatal(err)
	}
	if col.Fields.GetByName("canManage") == nil {
		t.Error("the canManage field is missing from a freshly created collection")
	}
}

// TestManageContractShape pins the exact set of keys the contract publishes.
// A field added to the record must not appear here by accident: the whole point
// of the contract is that the schema can move without breaking a caller.
func TestManageContractShape(t *testing.T) {
	app, dir, _ := manageApp(t)
	s := manageScenario(app, dir, tests.ApiScenario{
		Name:               "contract keys",
		Method:             http.MethodGet,
		URL:                "/api/faasbox/functions/echo",
		Headers:            manageKeyHeader(t, app, "manager", nil),
		ExpectedStatus:     200,
		NotExpectedContent: []string{`"env"`, `"bunLock"`},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			var body map[string]json.RawMessage
			if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
				t.Fatalf("failed to decode the response: %v", err)
			}
			want := []string{"id", "name", "script", "packageJson", "depsStatus", "depsError", "crons"}
			if len(body) != len(want) {
				t.Errorf("the contract carries %d keys (%v), want %d", len(body), body, len(want))
			}
			for _, key := range want {
				if _, ok := body[key]; !ok {
					t.Errorf("the contract is missing %q", key)
				}
			}
		},
	})
	s.Test(t)
}

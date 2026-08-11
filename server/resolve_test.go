package main

import (
	"errors"
	"net/http"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

func TestResolveFunction(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	functionsDir := t.TempDir()
	echo := saveTestFunction(t, app, functionsDir, "echo", "console.log(1)", "")

	t.Run("by name", func(t *testing.T) {
		got, err := resolveFunction(app, "echo")
		if err != nil {
			t.Fatalf("resolveFunction() error: %v", err)
		}
		if got.Id != echo.Id {
			t.Errorf("id = %q, want %q", got.Id, echo.Id)
		}
	})

	t.Run("by id", func(t *testing.T) {
		got, err := resolveFunction(app, echo.Id)
		if err != nil {
			t.Fatalf("resolveFunction() error: %v", err)
		}
		if got.Id != echo.Id {
			t.Errorf("id = %q, want %q", got.Id, echo.Id)
		}
	})

	// An id-shaped segment matching no record is not a dead end: it is looked up
	// as a name like anything else.
	t.Run("an id-shaped segment falls back to the name", func(t *testing.T) {
		named := saveTestFunctionAs(t, app, functionsDir, "", "abcdefghij12345", "console.log(1)", "")
		got, err := resolveFunction(app, "abcdefghij12345")
		if err != nil {
			t.Fatalf("resolveFunction() error: %v", err)
		}
		if got.Id != named.Id {
			t.Errorf("id = %q, want the record named after an id shape, %q", got.Id, named.Id)
		}
	})

	// The tie-break, and the reason the rule is spelled out rather than guessed
	// from the shape of the segment: validName accepts both spellings, so a name
	// can be another function's id.
	t.Run("the id wins over a name that collides with it", func(t *testing.T) {
		const collision = "collisionfn0001"
		byId := saveTestFunctionAs(t, app, functionsDir, collision, "holder", "console.log(1)", "")
		byName := saveTestFunction(t, app, functionsDir, collision, "console.log(2)", "")

		got, err := resolveFunction(app, collision)
		if err != nil {
			t.Fatalf("resolveFunction() error: %v", err)
		}
		if got.Id != byId.Id {
			t.Errorf("resolved %q, want the record carrying that id, %q", got.Id, byId.Id)
		}
		if got.Id == byName.Id {
			t.Error("the name won over the id: the function named after it is reachable, the one carrying it is not")
		}
	})

	t.Run("an unknown segment is a not found", func(t *testing.T) {
		_, err := resolveFunction(app, "ghost")
		var notFound *errNotFound
		if !errors.As(err, &notFound) {
			t.Fatalf("error = %v, want *errNotFound", err)
		}
	})

	t.Run("an empty segment is a not found", func(t *testing.T) {
		_, err := resolveFunction(app, "")
		var notFound *errNotFound
		if !errors.As(err, &notFound) {
			t.Fatalf("error = %v, want *errNotFound", err)
		}
	})
}

// TestInvokeHandler_ResolvesEitherSpelling is the route-level half: an
// integration wired on /invoke/echo keeps working, and one wired on the id gets
// the same function.
func TestInvokeHandler_ResolvesEitherSpelling(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	key := createTestAPIKey(t, app, "resolve", []string{"*"})
	functionsDir, functions := setupTestFunctions(t, app, map[string]string{
		"echo": `console.log(JSON.stringify({ ok: true }));`,
	})

	for _, segment := range []string{"echo", functions["echo"].Id} {
		scenario := tests.ApiScenario{
			Name:                  "invoke by " + segment,
			Method:                http.MethodPost,
			URL:                   "/invoke/" + segment,
			Headers:               map[string]string{"X-API-Key": key},
			TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
			DisableTestAppCleanup: true,
			BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
				registerFaaSRoutes(app, e, functionsDir)
			},
			ExpectedStatus: 200,
			// The response names the function, never the segment that reached it.
			ExpectedContent: []string{`"function":"echo"`, `"ok":true`},
		}
		scenario.Test(t)
	}
}

// TestRequireAPIKey_ScopeFollowsARename pins why a scope holds ids and not
// names. A scope written in names stopped granting what it named the day the
// function was renamed — and could start granting a different function created
// since under the old name.
func TestRequireAPIKey_ScopeFollowsARename(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	functionsDir, functions := setupTestFunctions(t, app, map[string]string{"echo": ""})
	echo := functions["echo"]
	key := createTestAPIKey(t, app, "scoped", []string{echo.Id})

	echo.Set("name", "echo-renamed")
	if err := app.Save(echo); err != nil {
		t.Fatal(err)
	}

	scenario := tests.ApiScenario{
		Name:                  "the scoped key still invokes the renamed function",
		Method:                http.MethodPost,
		URL:                   "/invoke/echo-renamed",
		Headers:               map[string]string{"X-API-Key": key},
		TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
		DisableTestAppCleanup: true,
		BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
			registerFaaSRoutes(app, e, functionsDir)
		},
		ExpectedStatus:     200,
		NotExpectedContent: []string{`not authorized`},
	}
	scenario.Test(t)
}

// TestRequireAPIKey_RefusesAnotherFunctionsId covers the refusal in the spelling
// the scope itself uses: presenting an id reaches the check the same way a name
// does, and an id outside the scope is a 403 like any other.
func TestRequireAPIKey_RefusesAnotherFunctionsId(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	functionsDir, functions := setupTestFunctions(t, app, map[string]string{
		"mine":   "",
		"theirs": "",
	})
	key := createTestAPIKey(t, app, "scoped-mine", []string{functions["mine"].Id})

	scenario := tests.ApiScenario{
		Name:                  "an out-of-scope id is refused",
		Method:                http.MethodPost,
		URL:                   "/invoke/" + functions["theirs"].Id,
		Headers:               map[string]string{"X-API-Key": key},
		TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
		DisableTestAppCleanup: true,
		BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
			registerFaaSRoutes(app, e, functionsDir)
		},
		ExpectedStatus:  403,
		ExpectedContent: []string{`API key is not authorized`},
	}
	scenario.Test(t)
}

// TestRequireAPIKey_UnknownSegmentStaysRefusedForAScopedKey keeps the closed
// door closed. A scoped key must not be able to tell "exists but not yours" from
// "does not exist": that oracle is the inventory the scope filter withholds.
func TestRequireAPIKey_UnknownSegmentStaysRefusedForAScopedKey(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	functionsDir, functions := setupTestFunctions(t, app, map[string]string{"mine": ""})
	key := createTestAPIKey(t, app, "scoped-mine", []string{functions["mine"].Id})

	scenario := tests.ApiScenario{
		Name:                  "an unknown segment is a 403, not a 404",
		Method:                http.MethodPost,
		URL:                   "/invoke/ghost",
		Headers:               map[string]string{"X-API-Key": key},
		TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
		DisableTestAppCleanup: true,
		BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
			registerFaaSRoutes(app, e, functionsDir)
		},
		ExpectedStatus:  403,
		ExpectedContent: []string{`API key is not authorized`},
	}
	scenario.Test(t)
}

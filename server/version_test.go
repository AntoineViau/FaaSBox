package main

import (
	"net/http"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

func TestVersionHandler(t *testing.T) {
	setup := func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
		setupFaaSCollections(t, app)
		registerFaaSRoutes(app, e, t.TempDir())
	}

	scenarios := []tests.ApiScenario{
		{
			Name:           "a superuser reads the build version",
			Method:         http.MethodGet,
			URL:            "/api/faasbox/version",
			Headers:        map[string]string{"Authorization": superuserToken},
			BeforeTestFunc: setup,
			ExpectedStatus: 200,
			// A test binary is built without -ldflags, so it is the default.
			ExpectedContent: []string{`{"version":"dev"}`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				// A version governs nothing the interface allows, so the
				// no-store rule does not reach it.
				if got := res.Header.Get("Cache-Control"); got == "no-store" {
					t.Errorf("Cache-Control = %q, the version is not under the no-store rule", got)
				}
			},
		},
		{
			Name:            "no authentication is refused",
			Method:          http.MethodGet,
			URL:             "/api/faasbox/version",
			BeforeTestFunc:  setup,
			ExpectedStatus:  401,
			ExpectedContent: []string{`"data"`},
		},
	}

	for _, scenario := range scenarios {
		scenario.Test(t)
	}

	// The widest key there is — open scope, management flag — still opens
	// nothing here: the route asks for a superuser session, not a key.
	app, functionsDir, _ := manageApp(t)
	keyOnly := manageScenario(app, functionsDir, tests.ApiScenario{
		Name:            "an API key alone is refused",
		Method:          http.MethodGet,
		URL:             "/api/faasbox/version",
		Headers:         manageKeyHeader(t, app, "versionkey", unrestricted),
		ExpectedStatus:  401,
		ExpectedContent: []string{`"data"`},
	})
	keyOnly.Test(t)
}

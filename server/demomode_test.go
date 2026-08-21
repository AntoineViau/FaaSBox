package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// demoApp prepares an app carrying the four FaaSBox collections, which is what
// the refused routes below write into.
func demoApp(t testing.TB) *tests.TestApp {
	t.Helper()

	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)

	setupFaaSCollections(t, app)
	return app
}

// demoScenario wires a scenario onto a prepared app, in demo mode: the refusal
// on the root router, ahead of everything, then the instance route — exactly
// the order main.go uses.
//
// The binding lands on the root group, so it covers the routes PocketBase
// registered before this hook ran. That is the whole point of the placement,
// and every scenario aimed at /api/collections/... below is the proof of it.
func demoScenario(app *tests.TestApp, demo demoSettings, s tests.ApiScenario) tests.ApiScenario {
	s.TestAppFactory = func(t testing.TB) *tests.TestApp { return app }
	s.DisableTestAppCleanup = true
	s.BeforeTestFunc = func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
		if demo.Enabled {
			e.Router.BindFunc(refuseWrites)
		}
		e.Router.GET("/api/faasbox/instance", instanceHandler(demo))
		registerFaaSRoutes(app, e, t.TempDir())
	}
	return s
}

// demoOn is the showcase the scenarios below are written against.
var demoOn = demoSettings{Enabled: true, Email: "demo@faasbox.net", Password: "demo"}

// refusalBody is what a refused write answers, and the string every scenario
// that must *not* be refused checks for the absence of.
const refusalBody = `"error":"this instance is a read-only demo"`

// TestDemoModeLetsSafeMethodsThrough covers the three methods that change
// nothing: they reach their handler untouched.
func TestDemoModeLetsSafeMethodsThrough(t *testing.T) {
	cases := []struct {
		method string
		status int
	}{
		{http.MethodGet, 200},
		{http.MethodHead, 200},
		// No OPTIONS route is registered, so the request lands on the
		// catch-all PocketBase mounts and gets its 404 — which is a route
		// answering, not the middleware refusing.
		{http.MethodOptions, 404},
	}

	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			app := demoApp(t)
			s := demoScenario(app, demoOn, tests.ApiScenario{
				Name:               tc.method + " /health traverses the middleware",
				Method:             tc.method,
				URL:                "/health",
				ExpectedStatus:     tc.status,
				NotExpectedContent: []string{refusalBody},
			})
			s.Test(t)
		})
	}
}

// TestDemoModeRefusesEveryOtherMethod covers the default: anything the table of
// exceptions does not name is refused, whatever the route.
func TestDemoModeRefusesEveryOtherMethod(t *testing.T) {
	cases := []struct {
		name, method, url string
	}{
		{"invocation", http.MethodPost, "/invoke/echo"},
		{"function record", http.MethodPost, "/api/collections/" + faasboxFunctionsCollection + "/records"},
		{"MCP", http.MethodPost, "/mcp"},
		{"function record update", http.MethodPatch, "/api/collections/" + faasboxFunctionsCollection + "/records/abc"},
		{"function record deletion", http.MethodDelete, "/api/collections/" + faasboxFunctionsCollection + "/records/abc"},
		{"management route", http.MethodPut, "/api/faasbox/functions/echo"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := demoApp(t)
			s := demoScenario(app, demoOn, tests.ApiScenario{
				Name:            tc.method + " " + tc.url + " is refused",
				Method:          tc.method,
				URL:             tc.url,
				Body:            strings.NewReader(`{}`),
				ExpectedStatus:  403,
				ExpectedContent: []string{refusalBody},
			})
			s.Test(t)
		})
	}
}

// TestDemoModeSignInGoesThrough is the exception the whole mode rests on: the
// visitor still opens an ordinary session.
func TestDemoModeSignInGoesThrough(t *testing.T) {
	app := demoApp(t)
	s := demoScenario(app, demoOn, tests.ApiScenario{
		Name:               "sign in",
		Method:             http.MethodPost,
		URL:                "/api/collections/_superusers/auth-with-password",
		Body:               strings.NewReader(`{"identity":"test@example.com","password":"1234567890"}`),
		ExpectedStatus:     200,
		ExpectedContent:    []string{`"token":"`},
		NotExpectedContent: []string{refusalBody},
	})
	s.Test(t)
}

// TestDemoModeSessionRefreshGoesThrough covers the second exception: the SPA
// validates the session it restored from localStorage this way.
func TestDemoModeSessionRefreshGoesThrough(t *testing.T) {
	app := demoApp(t)
	s := demoScenario(app, demoOn, tests.ApiScenario{
		Name:               "refresh the session",
		Method:             http.MethodPost,
		URL:                "/api/collections/_superusers/auth-refresh",
		Headers:            map[string]string{"Authorization": superuserToken},
		ExpectedStatus:     200,
		ExpectedContent:    []string{`"token":"`},
		NotExpectedContent: []string{refusalBody},
	})
	s.Test(t)
}

// TestDemoModeRealtimeSubscriptionGoesThrough covers the third: the editor
// places its subscriptions with a POST.
//
// The client id names nothing, so the handler answers 404 — which is the
// handler answering, and that is what proves the traversal.
func TestDemoModeRealtimeSubscriptionGoesThrough(t *testing.T) {
	app := demoApp(t)
	s := demoScenario(app, demoOn, tests.ApiScenario{
		Name:               "place the subscriptions",
		Method:             http.MethodPost,
		URL:                "/api/realtime",
		Body:               strings.NewReader(`{"clientId":"nonexistent"}`),
		ExpectedStatus:     404,
		NotExpectedContent: []string{refusalBody},
		ExpectedEvents:     map[string]int{"*": 0},
	})
	s.Test(t)
}

// TestWithoutDemoModeNothingIsRefused is the other half of the flag: without
// it, no request meets refuseWrites at all.
func TestWithoutDemoModeNothingIsRefused(t *testing.T) {
	app := demoApp(t)
	s := demoScenario(app, demoSettings{}, tests.ApiScenario{
		Name:   "a write on a normal instance never sees the middleware",
		Method: http.MethodPost,
		URL:    "/api/collections/" + faasboxFunctionsCollection + "/records",
		Body:   strings.NewReader(`{"name":"echo","script":"console.log(1)"}`),
		// PocketBase's own rule answers, the collection being superuser-only.
		// The status it picks happens to be 403 as well; the body is what tells
		// the two refusals apart, and it is not ours.
		ExpectedStatus:     403,
		NotExpectedContent: []string{refusalBody},
	})
	s.Test(t)
}

// TestInstancePublishesTheModeInDemoMode covers what the sign-in form reads.
func TestInstancePublishesTheModeInDemoMode(t *testing.T) {
	app := demoApp(t)
	s := demoScenario(app, demoOn, tests.ApiScenario{
		Name:           "the instance route in demo mode",
		Method:         http.MethodGet,
		URL:            "/api/faasbox/instance",
		ExpectedStatus: 200,
		ExpectedContent: []string{
			`"demoMode":true`,
			`"email":"demo@faasbox.net"`,
			`"password":"demo"`,
		},
		ExpectedEvents: map[string]int{"*": 0},
	})
	s.Test(t)
}

// TestInstanceHidesTheCredentialsOutsideDemoMode is the guard that matters: the
// two variables may be set on an instance where the flag is not, and the route
// must not publish them there.
func TestInstanceHidesTheCredentialsOutsideDemoMode(t *testing.T) {
	app := demoApp(t)
	s := demoScenario(app, demoSettings{Email: "demo@faasbox.net", Password: "demo"}, tests.ApiScenario{
		Name:               "the instance route on a normal instance",
		Method:             http.MethodGet,
		URL:                "/api/faasbox/instance",
		ExpectedStatus:     200,
		ExpectedContent:    []string{`"demoMode":false`},
		NotExpectedContent: []string{`"email"`, `"password"`, "demo@faasbox.net"},
		ExpectedEvents:     map[string]int{"*": 0},
	})
	s.Test(t)
}

// TestEnvBool locks the reading policy of the flag: the default when absent,
// the value when readable, an error naming both when it is not.
func TestEnvBool(t *testing.T) {
	const name = "FAASBOX_TEST_ENV_BOOL"

	valid := []struct {
		label string
		set   bool
		value string
		def   bool
		want  bool
	}{
		{"Unset falls back to false", false, "", false, false},
		{"Unset falls back to true", false, "", true, true},
		{"Empty falls back", true, "", false, false},
		{"true", true, "true", false, true},
		{"1", true, "1", false, true},
		{"TRUE", true, "TRUE", false, true},
		{"false", true, "false", true, false},
		{"0", true, "0", true, false},
	}
	for _, tt := range valid {
		t.Run(tt.label, func(t *testing.T) {
			if tt.set {
				t.Setenv(name, tt.value)
			}
			got, err := envBool(name, tt.def)
			if err != nil {
				t.Fatalf("envBool(%q=%q) failed: %v", name, tt.value, err)
			}
			if got != tt.want {
				t.Fatalf("envBool(%q=%q) = %v, want %v", name, tt.value, got, tt.want)
			}
		})
	}

	for _, value := range []string{"treu", "oui", "yes", " true", "2"} {
		t.Run("unreadable "+value, func(t *testing.T) {
			t.Setenv(name, value)
			_, err := envBool(name, false)
			if err == nil {
				t.Fatalf("envBool(%q=%q) returned no error", name, value)
			}
			// The message is what the operator reads on a server that just
			// refused to start: it has to name the variable and the value.
			if !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), value) {
				t.Fatalf("error %q names neither %q nor %q", err, name, value)
			}
		})
	}
}

// TestNoCollectionDeclaresAnAccessRule is the invariant the demo mode leans on
// and never touches.
//
// The five enriched collections hand decrypted columns to any serialized
// response, and that is only safe because PocketBase reserves them to the
// superuser for want of a rule. Opening one — to let a visitor read without a
// session, say — would turn the enrichment hook into a leak. The mode does not
// take that road: the visitor signs in instead.
//
// The ensure functions know nothing of the mode, so one pass covers both.
func TestNoCollectionDeclaresAnAccessRule(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()

	setupFaaSCollections(t, app)
	if err := ensureOAuthClientsCollection(app); err != nil {
		t.Fatalf("failed to create the OAuth clients collection: %v", err)
	}
	if err := ensureOAuthGrantsCollection(app); err != nil {
		t.Fatalf("failed to create the OAuth grants collection: %v", err)
	}

	names := []string{
		faasboxFunctionsCollection,
		faasboxAPIKeysCollection,
		faasboxTriggersCollection,
		faasboxLogsCollection,
		faasboxOAuthClientsCollection,
		faasboxOAuthGrantsCollection,
	}

	for _, name := range names {
		collection, err := app.FindCollectionByNameOrId(name)
		if err != nil {
			t.Fatalf("failed to read the %s collection: %v", name, err)
		}
		rules := map[string]*string{
			"ListRule":   collection.ListRule,
			"ViewRule":   collection.ViewRule,
			"CreateRule": collection.CreateRule,
			"UpdateRule": collection.UpdateRule,
			"DeleteRule": collection.DeleteRule,
		}
		for label, rule := range rules {
			if rule != nil {
				t.Errorf("%s.%s = %q, want nil", name, label, *rule)
			}
		}
	}
}

// setupOAuthCollections creates the two collections the OAuth routes write into.
func setupOAuthCollections(t testing.TB, app core.App) {
	t.Helper()
	if err := ensureOAuthClientsCollection(app); err != nil {
		t.Fatalf("failed to create the OAuth clients collection: %v", err)
	}
	if err := ensureOAuthGrantsCollection(app); err != nil {
		t.Fatalf("failed to create the OAuth grants collection: %v", err)
	}
}

// demoOAuthScenario is demoScenario with the OAuth routes mounted, the way
// main.go mounts them on an instance that names its public address — demo mode
// included, which is the whole point of the two tests below.
func demoOAuthScenario(app *tests.TestApp, s tests.ApiScenario) tests.ApiScenario {
	s.TestAppFactory = func(t testing.TB) *tests.TestApp { return app }
	s.DisableTestAppCleanup = true
	s.BeforeTestFunc = func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
		e.Router.BindFunc(refuseWrites)
		if err := mountOAuth(e, testOAuthConfig); err != nil {
			t.Fatalf("failed to mount the OAuth routes: %v", err)
		}
	}
	return s
}

// TestDemoModeRefusesTheAuthorizeRequest covers the one GET that writes.
//
// /oauth/authorize records a pending grant before it redirects, so the method
// rule alone would let a caller grow faasbox_oauth_grants without bound — the
// hourly prune that collected them is one of the five startups a showcase
// skips. The assertion that matters is the second one: nothing was written.
func TestDemoModeRefusesTheAuthorizeRequest(t *testing.T) {
	app := demoApp(t)
	setupOAuthCollections(t, app)
	client := seedOAuthClient(t, app, testRedirectURI)

	s := demoOAuthScenario(app, tests.ApiScenario{
		Name:            "an authorization request is refused",
		Method:          http.MethodGet,
		URL:             authorizeURL(oauthClientId(app, client), nil),
		ExpectedStatus:  403,
		ExpectedContent: []string{refusalBody},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			grants, err := app.FindAllRecords(faasboxOAuthGrantsCollection)
			if err != nil {
				t.Fatalf("failed to read the grants: %v", err)
			}
			if len(grants) != 0 {
				t.Fatalf("got %d grant records, want 0 — the refusal let a write through", len(grants))
			}
		},
	})
	s.Test(t)
}

// TestDemoModeKeepsTheOAuthReadsOpen is the other half of the arbitration: the
// OAuth routes stay mounted in demo mode, and the ones that only read stay
// reachable.
//
// Not mounting them at all would have closed the write too, and taken the
// discovery documents down with it — which is what the /agents page probes to
// decide whether to render the authorized-agents panel. The showcase would then
// hide a list it exists to display.
func TestDemoModeKeepsTheOAuthReadsOpen(t *testing.T) {
	for _, path := range []string{
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-authorization-server",
	} {
		t.Run(path, func(t *testing.T) {
			app := demoApp(t)
			setupOAuthCollections(t, app)
			s := demoOAuthScenario(app, tests.ApiScenario{
				Name:               "discovery document served in demo mode",
				Method:             http.MethodGet,
				URL:                path,
				ExpectedStatus:     200,
				ExpectedContent:    []string{"https://faas.test"},
				NotExpectedContent: []string{refusalBody},
			})
			s.Test(t)
		})
	}
}

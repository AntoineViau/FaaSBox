package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/logger"
)

// --- Configuration ---

func TestParsePagesURL(t *testing.T) {
	valid := []struct {
		name, raw, public, origin, host string
	}{
		{"http on a dev host", "http://pages.localhost:8080", "http://127.0.0.1:8080", "http://pages.localhost:8080", "pages.localhost:8080"},
		{"https, trailing slash", "https://pages.example.com/", "https://faas.example.com", "https://pages.example.com", "pages.example.com"},
		{"https default port removed", "https://pages.example.com:443", "", "https://pages.example.com", "pages.example.com"},
		{"http default port removed", "http://pages.example.com:80", "", "http://pages.example.com", "pages.example.com"},
		{"a port that is not the default kept", "https://pages.example.com:8443", "", "https://pages.example.com:8443", "pages.example.com:8443"},
		// OAuth's rule on http:// is OAuth's alone.
		{"http off loopback accepted", "http://pages.example.com", "", "http://pages.example.com", "pages.example.com"},
		// A public URL that does not parse compares with nothing.
		{"public URL unparsable", "https://pages.example.com", "ftp://x", "https://pages.example.com", "pages.example.com"},
		// Same host name, another port: another origin, and localStorage is per origin.
		{"same name, another port", "http://localhost:8081", "http://localhost:8080", "http://localhost:8081", "localhost:8081"},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parsePagesURL(tc.raw, tc.public)
			if err != nil {
				t.Fatalf("parsePagesURL(%q) refused: %v", tc.raw, err)
			}
			if cfg.Origin != tc.origin || cfg.Host != tc.host {
				t.Errorf("config = %+v, want origin %q host %q", cfg, tc.origin, tc.host)
			}
		})
	}

	refused := []struct {
		name, raw, public, reason string
	}{
		{"absent", "", "", "is not set"},
		{"not absolute", "pages.example.com", "", "absolute"},
		{"carries a path", "https://pages.example.com/f", "", "path"},
		{"carries a query", "https://pages.example.com?a=1", "", "query"},
		{"carries a fragment", "https://pages.example.com#x", "", "fragment"},
		{"carries credentials", "https://u:p@pages.example.com", "", "credentials"},
		{"unknown scheme", "ftp://pages.example.com", "", "absolute"},
		{"the editor's host", "https://faas.example.com", "https://faas.example.com", "host of their own"},
		{"the editor's host, other casing", "https://FAAS.example.com", "https://faas.example.com/", "host of their own"},
		{"the editor's host, default port spelled", "https://faas.example.com:443", "https://faas.example.com", "host of their own"},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parsePagesURL(tc.raw, tc.public)
			if err == nil {
				t.Fatalf("parsePagesURL(%q) was accepted, want a refusal", tc.raw)
			}
			if !strings.Contains(err.Error(), tc.reason) || !strings.Contains(err.Error(), pagesURLEnv) {
				t.Errorf("refusal %q does not name %s and %q", err, pagesURLEnv, tc.reason)
			}
		})
	}
}

// TestPagesConfigFromEnv_DemoServesNothing covers the showcase: the variable is
// set and valid, and nothing is mounted all the same.
func TestPagesConfigFromEnv_DemoServesNothing(t *testing.T) {
	t.Setenv(pagesURLEnv, "http://pages.localhost:8080")
	if _, err := pagesConfigFromEnv(false); err != nil {
		t.Fatalf("a valid %s was refused: %v", pagesURLEnv, err)
	}
	_, err := pagesConfigFromEnv(true)
	if err == nil || !strings.Contains(err.Error(), "demo") {
		t.Fatalf("pagesConfigFromEnv(demo) = %v, want a refusal naming the demo", err)
	}
}

// TestReportPagesDisabled covers the startup line: it is the only thing that
// explains a /f/ answering 404, so it has to carry the reason.
func TestReportPagesDisabled(t *testing.T) {
	out := captureLog(t)
	_, err := parsePagesURL("", "")
	reportPagesDisabled(err)
	if got := out.String(); !strings.Contains(got, "public pages are disabled") || !strings.Contains(got, pagesURLEnv+" is not set") {
		t.Errorf("startup line = %q, want the reason", got)
	}
}

// --- Fixtures ---

const pagesTestHost = "pages.test"

var pagesTestConfig = pagesConfig{Origin: "http://" + pagesTestHost, Host: pagesTestHost}

// pageURL addresses a path on the pages host: an absolute URL is what makes
// httptest set the Host the gate reads.
func pageURL(path string) string { return "http://" + pagesTestHost + path }

// echoPageScript answers with the envelope it was handed, as a JSON page.
const echoPageScript = `const req = await Bun.stdin.text();
console.log(JSON.stringify({headers: {"content-type": "application/json", "x-page": "yes"}, body: req}));`

// pagesApp prepares an app with one function, "page", opened by an active route.
func pagesApp(t testing.TB, script string) (*tests.TestApp, string, *core.Record) {
	t.Helper()
	withEncryptionKey(t)

	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)

	setupFaaSCollections(t, app)
	bindTriggerHook(app)

	dir := t.TempDir()
	fn := saveTestFunction(t, app, dir, "page", script, "")
	createTestRouteTrigger(t, app, "public", fn.Id, true)
	return app, dir, fn
}

// createTestRouteTrigger saves a route trigger on a function.
func createTestRouteTrigger(t testing.TB, app core.App, name, functionId string, active bool) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId(faasboxTriggersCollection)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(col)
	record.Set("name", name)
	record.Set("kind", "route")
	record.Set("function", functionId)
	record.Set("active", active)
	if err := app.Save(record); err != nil {
		t.Fatalf("failed to create route trigger %q: %v", name, err)
	}
	return record
}

// pagesScenario wires a scenario the way main.go wires a server with pages: the
// gate and /f/, then every other route, then two stand-ins for what PocketBase's
// serve and main.go add last — the admin at /_/ and the SPA catch-all. They
// answer 200, so a 404 on them is the gate's and nobody else's; the catch-all
// is also what Any("/f/{name}") would have panicked against.
func pagesScenario(app *tests.TestApp, dir string, s tests.ApiScenario) *tests.ApiScenario {
	return pagesScenarioWith(app, dir, pagesTestConfig, s)
}

// pagesScenarioWith is pagesScenario on a chosen configuration — noPages for an
// instance that serves none.
func pagesScenarioWith(app *tests.TestApp, dir string, cfg pagesConfig, s tests.ApiScenario) *tests.ApiScenario {
	s.TestAppFactory = func(t testing.TB) *tests.TestApp { return app }
	s.DisableTestAppCleanup = true
	s.BeforeTestFunc = func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
		mountPages(e, cfg, dir)
		registerFaaSRoutes(app, e, dir)
		e.Router.GET("/_/{path...}", func(re *core.RequestEvent) error { return re.String(200, "admin") })
		e.Router.GET("/{path...}", func(re *core.RequestEvent) error { return re.String(200, "spa") })
	}
	return &s
}

// --- The host gate ---

// TestPagesHostGate_OnlyFAnswersOnThePagesHost covers the first direction: the
// editor, PocketBase's API, the admin and the sign-in all vanish from the pages
// host. Each is checked to answer elsewhere first, so the 404 is the gate's.
func TestPagesHostGate_OnlyFAnswersOnThePagesHost(t *testing.T) {
	cases := []struct {
		method, path, body string
		elsewhere          int
		answer             string
	}{
		{http.MethodGet, "/", "", 200, "spa"},
		{http.MethodGet, "/editor", "", 200, "spa"},
		{http.MethodGet, "/api/health", "", 200, "API is healthy"},
		{http.MethodGet, "/_/", "", 200, "admin"},
		{http.MethodPost, "/api/collections/_superusers/auth-with-password", `{"identity":"x@example.com","password":"y"}`, 400, "Failed to authenticate"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			app, dir, _ := pagesApp(t, echoPageScript)
			pagesScenario(app, dir, tests.ApiScenario{
				Name:            tc.path + " on the main host",
				Method:          tc.method,
				URL:             "http://faas.test" + tc.path,
				Body:            strings.NewReader(tc.body),
				ExpectedStatus:  tc.elsewhere,
				ExpectedContent: []string{tc.answer},
			}).Test(t)

			pagesScenario(app, dir, tests.ApiScenario{
				Name:            tc.path + " on the pages host",
				Method:          tc.method,
				URL:             pageURL(tc.path),
				Body:            strings.NewReader(tc.body),
				ExpectedStatus:  404,
				ExpectedContent: []string{`"error":"not found"`},
				ExpectedEvents:  map[string]int{"*": 0},
			}).Test(t)
		})
	}
}

// TestPagesHostGate_OnlyOneSegment covers the paths under /f/ that no
// /f/{name} route matches: without the gate they would reach the SPA catch-all,
// which is the editor served from the exposed origin. /f%2Fpage is the one a
// reading of the decoded path would let through: ServeMux sees a single
// segment, "f/page", that only the catch-all matches.
func TestPagesHostGate_OnlyOneSegment(t *testing.T) {
	for _, path := range []string{"/f/", "/f/page/", "/f/page/extra", "/f%2Fpage"} {
		app, dir, _ := pagesApp(t, echoPageScript)
		pagesScenario(app, dir, tests.ApiScenario{
			Name:               path + " on the pages host",
			Method:             http.MethodGet,
			URL:                pageURL(path),
			ExpectedStatus:     404,
			ExpectedContent:    []string{`"error":"not found"`},
			NotExpectedContent: []string{"spa"},
		}).Test(t)
	}
}

// TestPagesHostGate_FIsClosedElsewhere covers the second direction: a published
// function is not reachable on the main host, and nothing runs.
func TestPagesHostGate_FIsClosedElsewhere(t *testing.T) {
	app, dir, _ := pagesApp(t, echoPageScript)
	pagesScenario(app, dir, tests.ApiScenario{
		Name:               "/f/ on the main host",
		Method:             http.MethodGet,
		URL:                "http://faas.test/f/page",
		ExpectedStatus:     404,
		ExpectedContent:    []string{`"error":"not found"`},
		NotExpectedContent: []string{"trigger"},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			if got := countExecutionLogs(t, app, "page"); got != 0 {
				t.Errorf("execution logs = %d, want none: the function must not have run", got)
			}
		},
	}).Test(t)
}

// TestPagesHostGate_EncodedFIsClosedElsewhere covers the other encoded case:
// ServeMux unescapes each segment before matching, so /%66/page is /f/{name}
// itself, and it must not run a function on the main host.
func TestPagesHostGate_EncodedFIsClosedElsewhere(t *testing.T) {
	app, dir, _ := pagesApp(t, echoPageScript)
	pagesScenario(app, dir, tests.ApiScenario{
		Name:               "/%66/page on the main host",
		Method:             http.MethodGet,
		URL:                "http://faas.test/%66/page",
		ExpectedStatus:     404,
		ExpectedContent:    []string{`"error":"not found"`},
		NotExpectedContent: []string{"trigger"},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			if got := countExecutionLogs(t, app, "page"); got != 0 {
				t.Errorf("execution logs = %d, want none: the function must not have run", got)
			}
		},
	}).Test(t)
}

// TestPagesHostGate_WithoutPages covers an instance that serves no page: no /f/
// route exists, and the gate is what answers 404 there, on every host and every
// method, instead of the SPA catch-all showing the editor to whoever followed a
// link to a page no longer served. The rest of the instance answers as before.
func TestPagesHostGate_WithoutPages(t *testing.T) {
	app, dir, _ := pagesApp(t, echoPageScript)
	for _, tc := range []struct{ method, url string }{
		{http.MethodGet, "http://faas.test/f/page"},
		{http.MethodGet, pageURL("/f/page")},
		{http.MethodGet, "http://faas.test/f/page/extra"},
		{http.MethodPost, "http://faas.test/f/page"},
	} {
		pagesScenarioWith(app, dir, noPages, tests.ApiScenario{
			Name:               tc.method + " " + tc.url + " without pages",
			Method:             tc.method,
			URL:                tc.url,
			ExpectedStatus:     404,
			NotExpectedContent: []string{"spa", "trigger"},
		}).Test(t)
	}
	pagesScenarioWith(app, dir, noPages, tests.ApiScenario{
		Name:            "the editor still answers",
		Method:          http.MethodGet,
		URL:             "http://faas.test/",
		ExpectedStatus:  200,
		ExpectedContent: []string{"spa"},
	}).Test(t)

	if got := countExecutionLogs(t, app, "page"); got != 0 {
		t.Errorf("execution logs = %d, want none: nothing may run without pages", got)
	}
}

// TestPagesHostGate_CaseInsensitive covers the comparison: host names are not
// case-sensitive, and a visitor typing capitals reaches the same page.
func TestPagesHostGate_CaseInsensitive(t *testing.T) {
	app, dir, _ := pagesApp(t, echoPageScript)
	pagesScenario(app, dir, tests.ApiScenario{
		Name:            "/f/ on the pages host in capitals",
		Method:          http.MethodGet,
		URL:             "http://PAGES.Test/f/page",
		ExpectedStatus:  200,
		ExpectedContent: []string{`"trigger":"route"`},
	}).Test(t)
}

// --- The route ---

// TestPage_EveryMethodReachesTheFunction covers the six methods, with no key and
// no session, each recorded as a route run.
func TestPage_EveryMethodReachesTheFunction(t *testing.T) {
	app, dir, _ := pagesApp(t, echoPageScript)
	methods := []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	for _, method := range methods {
		// HEAD included: net/http drops the body on the wire, the recorder of
		// the test keeps it, and it shows the function ran with that method.
		pagesScenario(app, dir, tests.ApiScenario{
			Name:            method + " /f/page",
			Method:          method,
			URL:             pageURL("/f/page"),
			ExpectedStatus:  200,
			ExpectedContent: []string{`"trigger":"route"`, `"method":"` + method + `"`, `"path":"/f/page"`},
		}).Test(t)
	}

	entries := executionLogsOf(t, app, "page")
	if len(entries) != len(methods) {
		t.Fatalf("execution logs = %d, want one per method", len(entries))
	}
	for _, entry := range entries {
		if got := entry.GetString("trigger"); got != triggerRoute {
			t.Errorf("trigger = %q, want %q", got, triggerRoute)
		}
	}
}

// TestPage_ResolvesByIdAndByName covers the two handles of a function.
func TestPage_ResolvesByIdAndByName(t *testing.T) {
	app, dir, fn := pagesApp(t, echoPageScript)
	for _, segment := range []string{"page", fn.Id} {
		pagesScenario(app, dir, tests.ApiScenario{
			Name:            "/f/" + segment,
			Method:          http.MethodGet,
			URL:             pageURL("/f/" + segment),
			ExpectedStatus:  200,
			ExpectedContent: []string{`"trigger":"route"`},
		}).Test(t)
	}
}

// TestPage_UnpublishedIsAbsent covers the 404s: a function with no route, a
// route switched off, a malformed segment and an absent function all answer the
// same body, and none of them runs anything.
func TestPage_UnpublishedIsAbsent(t *testing.T) {
	app, dir, _ := pagesApp(t, echoPageScript)
	saveTestFunction(t, app, dir, "private", echoPageScript, "")
	off := saveTestFunction(t, app, dir, "switched-off", echoPageScript, "")
	createTestRouteTrigger(t, app, "public", off.Id, false)
	// A cron trigger opens nothing either.
	cronOnly := saveTestFunction(t, app, dir, "scheduled", echoPageScript, "")
	createTestTrigger(t, app, "nightly", "0 3 * * *", cronOnly.Id, true)

	for _, segment := range []string{"private", "switched-off", "scheduled", "bad_name", "absent"} {
		pagesScenario(app, dir, tests.ApiScenario{
			Name:            "/f/" + segment,
			Method:          http.MethodGet,
			URL:             pageURL("/f/" + segment),
			ExpectedStatus:  404,
			ExpectedContent: []string{`{"error":"not found"}`},
		}).Test(t)
	}

	all, err := app.FindAllRecords(faasboxLogsCollection)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("faasbox_logs holds %d entries, want none: nothing ran", len(all))
	}
}

// TestPage_EnvelopeOnStdin covers what the function receives: the HTTP envelope
// with "trigger": "route", the body verbatim, and none of the four headers that
// authenticate a caller — on stdin and in the log alike.
func TestPage_EnvelopeOnStdin(t *testing.T) {
	app, dir, _ := pagesApp(t, echoPageScript)
	pagesScenario(app, dir, tests.ApiScenario{
		Name:   "POST /f/page with credentials",
		Method: http.MethodPost,
		URL:    pageURL("/f/page?x=1"),
		Body:   strings.NewReader(`{"signed":true}`),
		Headers: map[string]string{
			"X-Api-Key":           "fbx_secret-key",
			"Authorization":       "Bearer secret-bearer",
			"Proxy-Authorization": "Basic secret-proxy",
			"Cookie":              "session=secret-cookie",
			"X-Custom":            "kept",
		},
		ExpectedStatus: 200,
		ExpectedContent: []string{
			`"trigger":"route"`,
			`"query":{"x":"1"}`,
			`"x-custom":"kept"`,
			`"body":"{\"signed\":true}"`,
		},
		NotExpectedContent: []string{"secret-key", "secret-bearer", "secret-proxy", "secret-cookie"},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			entries := executionLogsOf(t, app, "page")
			if len(entries) != 1 {
				t.Fatalf("execution logs = %d, want 1", len(entries))
			}
			payload := decryptedJSON(app, entries[0], "requestPayload")
			if !strings.Contains(payload, `"trigger":"route"`) {
				t.Errorf("requestPayload = %s, want the route envelope", payload)
			}
			for _, secret := range []string{"secret-key", "secret-bearer", "secret-proxy", "secret-cookie"} {
				if strings.Contains(payload, secret) {
					t.Errorf("requestPayload carries %q", secret)
				}
			}
		},
	}).Test(t)
}

// TestPage_BodyBounds covers the shared reading: the same 413 and the same 400
// as /invoke.
func TestPage_BodyBounds(t *testing.T) {
	app, dir, _ := pagesApp(t, echoPageScript)

	original := maxBodySize
	maxBodySize = 8
	t.Cleanup(func() { maxBodySize = original })

	pagesScenario(app, dir, tests.ApiScenario{
		Name:            "oversized body",
		Method:          http.MethodPost,
		URL:             pageURL("/f/page"),
		Body:            strings.NewReader("0123456789"),
		ExpectedStatus:  413,
		ExpectedContent: []string{"request body exceeds 8 bytes"},
	}).Test(t)

	pagesScenario(app, dir, tests.ApiScenario{
		Name:            "body that is not UTF-8",
		Method:          http.MethodPost,
		URL:             pageURL("/f/page"),
		Body:            strings.NewReader("\xff\xfe"),
		ExpectedStatus:  400,
		ExpectedContent: []string{"request body must be valid UTF-8"},
	}).Test(t)
}

// --- The response ---

// TestPage_EnvelopeIsApplied covers a page end to end: status, headers, and the
// body raw, with no JSON around it and nothing escaped.
func TestPage_EnvelopeIsApplied(t *testing.T) {
	app, dir, _ := pagesApp(t,
		`console.log(JSON.stringify({status: 201, headers: {"content-type": "text/html; charset=utf-8", "x-page": "yes"}, body: "<h1>\"Hello\" & bye</h1>"}))`)
	pagesScenario(app, dir, tests.ApiScenario{
		Name:            "a page",
		Method:          http.MethodGet,
		URL:             pageURL("/f/page"),
		ExpectedStatus:  201,
		ExpectedContent: []string{`<h1>"Hello" & bye</h1>`},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			assertPageResponse(t, res, "text/html; charset=utf-8", `<h1>"Hello" & bye</h1>`)
			if got := res.Header.Get("X-Page"); got != "yes" {
				t.Errorf("x-page = %q, want yes", got)
			}
		},
	}).Test(t)
}

// TestPage_Defaults covers the two defaults and base64 on the wire.
func TestPage_Defaults(t *testing.T) {
	app, dir, _ := pagesApp(t, `console.log(JSON.stringify({body: "<b>plain</b>"}))`)
	pagesScenario(app, dir, tests.ApiScenario{
		Name:            "no status, no content-type",
		Method:          http.MethodGet,
		URL:             pageURL("/f/page"),
		ExpectedStatus:  200,
		ExpectedContent: []string{"<b>plain</b>"},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			assertPageResponse(t, res, defaultResponseContentType, "<b>plain</b>")
		},
	}).Test(t)

	app, dir, _ = pagesApp(t, `console.log(JSON.stringify({body: "aGk=", bodyEncoding: "base64"}))`)
	pagesScenario(app, dir, tests.ApiScenario{
		Name:            "base64 body",
		Method:          http.MethodGet,
		URL:             pageURL("/f/page"),
		ExpectedStatus:  200,
		ExpectedContent: []string{"hi"},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			assertPageResponse(t, res, defaultResponseContentType, "hi")
		},
	}).Test(t)
}

// TestPage_RefusedEnvelope covers the 502: the rule named, nothing of stdout or
// stderr in the body, the run logged as an error, and the reason in the server
// log beside the function's name.
func TestPage_RefusedEnvelope(t *testing.T) {
	app, dir, _ := pagesApp(t, `console.error("secret-stderr"); console.log("secret-stdout")`)
	enableServerLogs(t, app)
	pagesScenario(app, dir, tests.ApiScenario{
		Name:               "plain text instead of an envelope",
		Method:             http.MethodGet,
		URL:                pageURL("/f/page"),
		ExpectedStatus:     502,
		ExpectedContent:    []string{"valid response envelope", "one JSON object"},
		NotExpectedContent: []string{"secret-stdout", "secret-stderr"},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			entries := executionLogsOf(t, app, "page")
			if len(entries) != 1 || entries[0].GetString("status") != "error" {
				t.Fatalf("execution logs = %d, want one entry in error", len(entries))
			}
			if !serverLogHas(t, app, "faasbox route: response envelope refused", "function", "page") {
				t.Error("the refusal is not in the server log with the function's name")
			}
		},
	}).Test(t)
}

// TestPage_TruncatedOutputIsNeverServed covers the capture cap: a page cut short
// is a 502, never a partial page.
func TestPage_TruncatedOutputIsNeverServed(t *testing.T) {
	app, dir, _ := pagesApp(t, `console.log(JSON.stringify({body: "<p>" + "x".repeat(500) + "</p>"}))`)

	original := maxOutputSize
	maxOutputSize = 64
	t.Cleanup(func() { maxOutputSize = original })

	pagesScenario(app, dir, tests.ApiScenario{
		Name:               "a page past the capture cap",
		Method:             http.MethodGet,
		URL:                pageURL("/f/page"),
		ExpectedStatus:     502,
		ExpectedContent:    []string{"capture limit"},
		NotExpectedContent: []string{"<p>", "xxxx"},
	}).Test(t)
}

// TestPage_FailedRunAnswersAFixedBody covers the 500: what the function wrote
// and the error it died of stay out of the body.
func TestPage_FailedRunAnswersAFixedBody(t *testing.T) {
	app, dir, _ := pagesApp(t, `console.log("secret-stdout"); console.error("secret-stderr"); throw new Error("secret-error")`)
	pagesScenario(app, dir, tests.ApiScenario{
		Name:               "a function that throws",
		Method:             http.MethodGet,
		URL:                pageURL("/f/page"),
		ExpectedStatus:     500,
		ExpectedContent:    []string{`{"error":"function failed"}`},
		NotExpectedContent: []string{"secret", "exit status"},
	}).Test(t)
}

// TestPage_SlotsFull covers the 429: /f/ takes its slot in the same semaphore
// as /invoke and the cron, and refuses rather than waits.
func TestPage_SlotsFull(t *testing.T) {
	app, dir, _ := pagesApp(t, echoPageScript)
	for i := 0; i < cap(sem); i++ {
		sem <- struct{}{}
	}
	t.Cleanup(func() {
		for i := 0; i < cap(sem); i++ {
			<-sem
		}
	})

	pagesScenario(app, dir, tests.ApiScenario{
		Name:            "every slot taken",
		Method:          http.MethodGet,
		URL:             pageURL("/f/page"),
		ExpectedStatus:  429,
		ExpectedContent: []string{errTooBusy.Error()},
	}).Test(t)
}

// TestAnswerPageFailure covers the whole table, the timeout included — a real
// one would cost execTimeout. The outcome carries what a run would have
// written, and none of it may reach the body.
func TestAnswerPageFailure(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)

	outcome := invokeOutcome{Function: "page", Stdout: "secret-stdout", Stderr: "secret-stderr", Duration: time.Second}
	cases := []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{"invalid segment", errInvalidFunctionName, 404, `{"error":"not found"}`},
		{"absent", &errNotFound{Name: "page"}, 404, `{"error":"not found"}`},
		{"slots full", errTooBusy, 429, errTooBusy.Error()},
		{"lookup fault", errResolveFailed, 500, errResolveFailed.Error()},
		{"timeout", errExecTimedOut, 504, `{"error":"function timed out"}`},
		{"exit code", errors.New("exit status 1: secret-error"), 500, `{"error":"function failed"}`},
		{"truncated", &errOutputUnusable{limit: 64}, 502, "capture limit"},
		{"refused envelope", refuseResponseAbout("secret-field", "unknown field"), 502, "unknown field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			e := &core.RequestEvent{App: app}
			e.Request = httptest.NewRequest(http.MethodGet, pageURL("/f/page"), nil)
			e.Response = rec

			if err := answerPageFailure(e, outcome, tc.err); err != nil {
				t.Fatal(err)
			}
			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d", rec.Code, tc.status)
			}
			body := rec.Body.String()
			if !strings.Contains(body, tc.body) {
				t.Errorf("body = %s, want %s", body, tc.body)
			}
			if strings.Contains(body, "secret") {
				t.Errorf("body = %s carries what the function wrote", body)
			}
		})
	}
}

// --- The trigger ---

// TestRouteTrigger_RenameKeepsServing covers the relation: the route points at
// the function, so a rename moves the page to the new name and keeps the id.
func TestRouteTrigger_RenameKeepsServing(t *testing.T) {
	app, dir, fn := pagesApp(t, echoPageScript)
	fn.Set("name", "renamed")
	if err := app.Save(fn); err != nil {
		t.Fatal(err)
	}
	for _, segment := range []string{"renamed", fn.Id} {
		pagesScenario(app, dir, tests.ApiScenario{
			Name:            "/f/" + segment + " after the rename",
			Method:          http.MethodGet,
			URL:             pageURL("/f/" + segment),
			ExpectedStatus:  200,
			ExpectedContent: []string{`"trigger":"route"`},
		}).Test(t)
	}
	pagesScenario(app, dir, tests.ApiScenario{
		Name:            "the old name",
		Method:          http.MethodGet,
		URL:             pageURL("/f/page"),
		ExpectedStatus:  404,
		ExpectedContent: []string{"not found"},
	}).Test(t)
}

// TestRouteTrigger_GoesWithItsFunction covers the cascade.
func TestRouteTrigger_GoesWithItsFunction(t *testing.T) {
	app, _, fn := pagesApp(t, echoPageScript)
	if err := app.Delete(fn); err != nil {
		t.Fatal(err)
	}
	if left, _ := app.FindAllRecords(faasboxTriggersCollection); len(left) != 0 {
		t.Errorf("%d trigger(s) survived the function", len(left))
	}
}

// TestSyncAllCronJobs_SkipsRouteTriggers pins that a route never reaches the
// scheduler, by the explicit test on the kind.
func TestSyncAllCronJobs_SkipsRouteTriggers(t *testing.T) {
	app, dir, fn := pagesApp(t, echoPageScript)
	route, err := app.FindFirstRecordByData(faasboxTriggersCollection, "function", fn.Id)
	if err != nil {
		t.Fatal(err)
	}

	syncAllCronJobs(app, dir, context.Background())

	for _, job := range app.Cron().Jobs() {
		if job.Id() == cronJobPrefix+route.Id {
			t.Fatal("a route trigger was registered on the cron scheduler")
		}
	}
}

// TestManageContract_CarriesARoute covers the management API: a route goes in
// as written and comes back as stored.
func TestManageContract_CarriesARoute(t *testing.T) {
	app, _, _ := manageApp(t)
	created, err := createFunction(app, unrestricted, manageRequest{
		Name:     "published",
		Script:   "console.log(1)",
		Triggers: []manageTrigger{{Name: "public", Kind: "route"}},
	})
	if err != nil {
		t.Fatalf("createFunction() refused a route: %v", err)
	}
	if len(created.Triggers) != 1 || created.Triggers[0].Kind != "route" || !created.Triggers[0].Active {
		t.Fatalf("created triggers = %+v, want one active route", created.Triggers)
	}

	read, err := getFunction(app, unrestricted, "published")
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Triggers) != 1 || read.Triggers[0].Kind != "route" {
		t.Errorf("read triggers = %+v, want the route", read.Triggers)
	}
}

// --- The instance ---

func TestInstancePublishesThePagesURL(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pages pagesConfig
		want  string
	}{
		{"mounted", pagesTestConfig, `"pagesUrl":"http://pages.test"`},
		{"not mounted", noPages, `"pagesUrl":""`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := demoApp(t)
			s := tests.ApiScenario{
				Name:                  "the instance route, pages " + tc.name,
				Method:                http.MethodGet,
				URL:                   "/api/faasbox/instance",
				ExpectedStatus:        200,
				ExpectedContent:       []string{tc.want},
				TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
				DisableTestAppCleanup: true,
				BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
					e.Router.GET("/api/faasbox/instance", instanceHandler(demoSettings{}, "", tc.pages))
				},
			}
			s.Test(t)
		})
	}
}

// --- Helpers ---

// assertPageResponse checks what a browser receives: the content type and the
// body byte for byte, with the length we computed.
func assertPageResponse(t testing.TB, res *http.Response, contentType, body string) {
	t.Helper()
	if got := res.Header.Get("Content-Type"); got != contentType {
		t.Errorf("content-type = %q, want %q", got, contentType)
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != body {
		t.Errorf("body = %q, want %q", raw, body)
	}
	if got, want := res.Header.Get("Content-Length"), strconv.Itoa(len(body)); got != want {
		t.Errorf("content-length = %q, want %q", got, want)
	}
}

// serverLogHas reports whether app.Logger() received a message carrying one
// attribute with this value.
func serverLogHas(t testing.TB, app core.App, message, key, value string) bool {
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
	for _, l := range logs {
		if l.Message == message && l.Data[key] == value {
			return true
		}
	}
	return false
}

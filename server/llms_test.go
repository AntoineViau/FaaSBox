package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// The guide is exercised over the router, not only through llmsTxt: what an
// agent reads is what main.go mounts, and a configuration dropped on the way —
// the name, the pages — would only show there.

// llmsScenario wires the FaaS routes the way main.go does, with the instance
// name and the pages chosen.
func llmsScenario(app *tests.TestApp, instanceName string, pages pagesConfig, s tests.ApiScenario) *tests.ApiScenario {
	s.TestAppFactory = func(t testing.TB) *tests.TestApp { return app }
	s.DisableTestAppCleanup = true
	s.BeforeTestFunc = func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
		registerFaaSRoutesFor(app, e, t.TempDir(), apiKeysOnly, instanceName, pages)
	}
	return &s
}

// readGuide reads the body of a /llms.txt response.
func readGuide(t testing.TB, res *http.Response) string {
	t.Helper()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestLLMsTxt_ServedToAnyone covers the guide itself: public, plain text, the
// shape the llms.txt proposal opens on, the API an agent with curl needs, the
// contract, and the traps it has to be told since nothing merges for it here.
func TestLLMsTxt_ServedToAnyone(t *testing.T) {
	// The version is not public, and this route is: a distinctive value makes
	// its absence checkable. Set before the route is mounted, which is when the
	// guide is composed.
	previous := faasboxVersion
	faasboxVersion = "9.9.9-guide"
	t.Cleanup(func() { faasboxVersion = previous })

	app := demoApp(t)
	llmsScenario(app, "", noPages, tests.ApiScenario{
		Name:               "the guide, without a key",
		Method:             http.MethodGet,
		URL:                "/llms.txt",
		ExpectedStatus:     200,
		NotExpectedContent: []string{"9.9.9-guide"},
		ExpectedEvents:     map[string]int{"*": 0},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			if got, want := res.Header.Get("Content-Type"), "text/plain; charset=utf-8"; got != want {
				t.Errorf("content-type = %q, want %q", got, want)
			}
			body := readGuide(t, res)

			if !strings.HasPrefix(body, "# FaaSBox\n\n> ") {
				t.Errorf("the guide does not open on the title and a quoted summary:\n%.200s", body)
			}
			summary := body[:strings.Index(body, "\n## ")]
			if !strings.Contains(summary, "/mcp") {
				t.Errorf("the summary does not send an MCP client to /mcp:\n%s", summary)
			}

			for _, marker := range contractMarkers {
				if !strings.Contains(body, marker) {
					t.Errorf("the guide does not carry the contract marker %q", marker)
				}
			}

			for _, want := range []string{
				// how to call
				"X-API-Key", "canManage", "relative to the origin you fetched this file from",
				// the seven routes
				"GET    /functions",
				"POST   /api/faasbox/functions",
				"GET    /api/faasbox/functions/{idOrName}",
				"PUT    /api/faasbox/functions/{idOrName}",
				"DELETE /api/faasbox/functions/{idOrName}",
				"GET    /api/faasbox/functions/{idOrName}/logs",
				"POST   /invoke/{idOrName}",
				// the traps, said since nothing applies them in code here
				"PUT the whole object back",
				`"plainEnv" sent as an empty object deletes`,
				`"plainEnv" listing one variable deletes every other secret`,
				`"triggers" sent as [] deletes every trigger`,
				"whole execution history",
				"there is no undo",
				"never a live secret",
			} {
				if !strings.Contains(body, want) {
					t.Errorf("the guide does not say %q", want)
				}
			}
		},
	}).Test(t)
}

// TestLLMsTxt_NamesTheInstance covers the title: the one the MCP server
// announces, so an agent plugged into two boxes tells them apart either way.
func TestLLMsTxt_NamesTheInstance(t *testing.T) {
	app := demoApp(t)
	llmsScenario(app, "toto", noPages, tests.ApiScenario{
		Name:            "the guide of a named instance",
		Method:          http.MethodGet,
		URL:             "/llms.txt",
		ExpectedStatus:  200,
		ExpectedContent: []string{"# FaaSBox toto\n"},
	}).Test(t)
}

// TestLLMsTxt_TrustsNoHost covers what the guide refuses to write: an origin
// read off the request. Host and the forwarding headers are whatever the
// caller says.
func TestLLMsTxt_TrustsNoHost(t *testing.T) {
	app := demoApp(t)
	llmsScenario(app, "", noPages, tests.ApiScenario{
		Name:   "the guide asked for under a foreign host",
		Method: http.MethodGet,
		URL:    "http://evil.example/llms.txt",
		Headers: map[string]string{
			"X-Forwarded-Host":  "forwarded.example",
			"X-Forwarded-Proto": "https",
		},
		ExpectedStatus:     200,
		ExpectedContent:    []string{"# FaaSBox"},
		NotExpectedContent: []string{"evil.example", "forwarded.example"},
	}).Test(t)
}

// TestLLMsTxt_ClosesOnThePages covers the last paragraph, on both footings:
// the address a published function answers at, or the fact that there is none.
func TestLLMsTxt_ClosesOnThePages(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pages pagesConfig
		want  string
	}{
		{"with pages", pagesTestConfig, "http://pages.test/f/{idOrName}"},
		{"without pages", noPages, "serves no public page"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := demoApp(t)
			llmsScenario(app, "", tc.pages, tests.ApiScenario{
				Name:            "the guide " + tc.name,
				Method:          http.MethodGet,
				URL:             "/llms.txt",
				ExpectedStatus:  200,
				ExpectedContent: []string{tc.want},
				AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
					body := strings.TrimSpace(readGuide(t, res))
					if !strings.HasSuffix(body, pagesParagraph(tc.pages)) {
						t.Errorf("the guide does not close on the pages paragraph:\n...%s", body[len(body)-300:])
					}
				},
			}).Test(t)
		})
	}
}

// TestLLMsTxt_AbsentFromThePagesHost covers the gate: the pages host serves
// /f/ and nothing else, the guide included. It is checked to answer on the main
// host first, so the 404 is the gate's.
func TestLLMsTxt_AbsentFromThePagesHost(t *testing.T) {
	app, dir, _ := pagesApp(t, echoPageScript)
	pagesScenario(app, dir, tests.ApiScenario{
		Name:            "the guide on the main host",
		Method:          http.MethodGet,
		URL:             "http://faas.test/llms.txt",
		ExpectedStatus:  200,
		ExpectedContent: []string{"# FaaSBox"},
	}).Test(t)

	pagesScenario(app, dir, tests.ApiScenario{
		Name:               "the guide on the pages host",
		Method:             http.MethodGet,
		URL:                pageURL("/llms.txt"),
		ExpectedStatus:     404,
		ExpectedContent:    []string{`"error":"not found"`},
		NotExpectedContent: []string{"# FaaSBox"},
	}).Test(t)
}

// TestLLMsTxt_ServedInDemoMode covers the showcase: the route writes nothing,
// so the refusal of writes lets it through.
func TestLLMsTxt_ServedInDemoMode(t *testing.T) {
	app := demoApp(t)
	s := demoScenario(app, demoOn, tests.ApiScenario{
		Name:               "the guide on a showcase",
		Method:             http.MethodGet,
		URL:                "/llms.txt",
		ExpectedStatus:     200,
		ExpectedContent:    []string{"# FaaSBox"},
		NotExpectedContent: []string{refusalBody},
	})
	s.Test(t)
}

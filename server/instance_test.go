package main

import (
	"bytes"
	"log"
	"net/http"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/tests"
)

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
			`"name":""`,
		},
		ExpectedEvents: map[string]int{"*": 0},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			assertNoStore(t, res)
		},
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
		ExpectedContent:    []string{`"demoMode":false`, `"name":""`},
		NotExpectedContent: []string{`"email"`, `"password"`, "demo@faasbox.net"},
		ExpectedEvents:     map[string]int{"*": 0},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			assertNoStore(t, res)
		},
	})
	s.Test(t)
}

// TestInstancePublishesTheName covers the field the editor header reads. It is
// rendered on a normal instance, which is the case the credentials are not:
// a label is not a secret, and the client is spared a missing-value case.
func TestInstancePublishesTheName(t *testing.T) {
	app := demoApp(t)
	s := namedInstanceScenario(app, demoSettings{}, "toto", tests.ApiScenario{
		Name:               "the instance route on a named instance",
		Method:             http.MethodGet,
		URL:                "/api/faasbox/instance",
		ExpectedStatus:     200,
		ExpectedContent:    []string{`"demoMode":false`, `"name":"toto"`},
		NotExpectedContent: []string{`"email"`, `"password"`},
		ExpectedEvents:     map[string]int{"*": 0},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			assertNoStore(t, res)
		},
	})
	s.Test(t)
}

// TestInstancePublishesTheNameInDemoMode pins that the two facets are
// independent: a showcase can be named like any other instance.
func TestInstancePublishesTheNameInDemoMode(t *testing.T) {
	app := demoApp(t)
	s := namedInstanceScenario(app, demoOn, "toto", tests.ApiScenario{
		Name:           "the instance route on a named showcase",
		Method:         http.MethodGet,
		URL:            "/api/faasbox/instance",
		ExpectedStatus: 200,
		ExpectedContent: []string{
			`"demoMode":true`,
			`"name":"toto"`,
			`"email":"demo@faasbox.net"`,
		},
		ExpectedEvents: map[string]int{"*": 0},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			assertNoStore(t, res)
		},
	})
	s.Test(t)
}

// TestInstanceNameFromEnv locks the reading policy: the value when it is
// acceptable, the empty string when it is not, and never an error — this
// variable never stops the server.
func TestInstanceNameFromEnv(t *testing.T) {
	cases := []struct {
		name  string
		set   bool
		value string
		want  string
	}{
		{"Unset is unnamed", false, "", ""},
		{"Empty is unnamed", true, "", ""},
		{"A plain name is kept", true, "toto", "toto"},
		{"Digits and capitals are kept", true, "Box42", "Box42"},
		{"Dot, hyphen and underscore are kept", true, "my.box_1-a", "my.box_1-a"},
		{"A leading separator is kept", true, "-toto", "-toto"},
		{"A trailing separator is kept", true, "toto.", "toto."},
		{"Thirty-two characters are kept", true, strings.Repeat("a", 32), strings.Repeat("a", 32)},
		{"Thirty-three characters are refused", true, strings.Repeat("a", 33), ""},
		{"A space is refused", true, "to to", ""},
		{"Padding is refused, not trimmed", true, " toto ", ""},
		{"A slash is refused", true, "prod/box", ""},
		{"A colon is refused", true, "prod:box", ""},
		{"An accent is refused", true, "boîte", ""},
		{"Markup is refused", true, "<b>x</b>", ""},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv(instanceNameEnv, tt.value)
			}
			if got := instanceNameFromEnv(); got != tt.want {
				t.Errorf("instanceNameFromEnv() with %s=%q = %q, want %q",
					instanceNameEnv, tt.value, got, tt.want)
			}
		})
	}
}

// TestInstanceNameRefusalIsLogged pins that a refused value does not vanish
// silently: the only trace an operator gets is this line, so it has to name the
// variable and show what was refused.
func TestInstanceNameRefusalIsLogged(t *testing.T) {
	var out bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(previous) })

	t.Setenv(instanceNameEnv, "to to")
	if got := instanceNameFromEnv(); got != "" {
		t.Fatalf("instanceNameFromEnv() = %q, want an unnamed instance", got)
	}

	line := out.String()
	for _, want := range []string{instanceNameEnv, `"to to"`} {
		if !strings.Contains(line, want) {
			t.Errorf("log line %q does not carry %q", line, want)
		}
	}
}

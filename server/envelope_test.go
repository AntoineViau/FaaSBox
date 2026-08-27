package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// decodeEnvelope reads back what a builder produced. Every assertion below goes
// through it rather than comparing JSON texts: what the contract promises is the
// shape a function parses, not the order the fields were written in.
func decodeEnvelope(t testing.TB, in executionInput) map[string]any {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal([]byte(in.Envelope), &envelope); err != nil {
		t.Fatalf("envelope %q is not valid JSON: %v", in.Envelope, err)
	}
	return envelope
}

// envelopeStrings reads a flat object field — "headers" or "query" — as the
// string-to-string map the contract says it is.
func envelopeStrings(t testing.TB, envelope map[string]any, field string) map[string]string {
	t.Helper()
	raw, ok := envelope[field]
	if !ok {
		t.Fatalf("the envelope carries no %q", field)
	}
	object, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("%q = %v, want an object", field, raw)
	}
	flat := make(map[string]string, len(object))
	for key, value := range object {
		text, ok := value.(string)
		if !ok {
			t.Errorf("%s[%q] = %v, want a string", field, key, value)
			continue
		}
		flat[key] = text
	}
	return flat
}

// TestNewHTTPInput_CarriesTheWholeCall is the shape the contract promises on the
// HTTP path: what was called, how, with what around it — and the body as a
// string, exactly as it arrived.
func TestNewHTTPInput_CarriesTheWholeCall(t *testing.T) {
	body := []byte(`{"id":"evt_1","type":"invoice.paid"}`)
	r := httptest.NewRequest(http.MethodPost, "/invoke/stripe-webhook?dry=1", bytes.NewReader(body))
	r.Header.Set("Stripe-Signature", "t=1756290000,v1=8f3a")
	r.Header.Set("Content-Type", "application/json")

	in := newHTTPInput(r, body)
	if in.Trigger != triggerHTTP {
		t.Errorf("trigger recorded = %q, want %q", in.Trigger, triggerHTTP)
	}

	envelope := decodeEnvelope(t, in)
	for field, want := range map[string]string{
		"trigger": "http",
		"method":  http.MethodPost,
		// The path as called, and the query string is not part of it.
		"path": "/invoke/stripe-webhook",
		"body": string(body),
	} {
		if got := envelope[field]; got != want {
			t.Errorf("%s = %v, want %q", field, got, want)
		}
	}

	if got := envelopeStrings(t, envelope, "query")["dry"]; got != "1" {
		t.Errorf("query.dry = %q, want %q", got, "1")
	}

	headers := envelopeStrings(t, envelope, "headers")
	// Lowercased, because HTTP declares header names case-insensitive and a
	// function must not have to guess the spelling that arrived.
	if got := headers["stripe-signature"]; got != "t=1756290000,v1=8f3a" {
		t.Errorf("headers[stripe-signature] = %q, want the signature as sent", got)
	}
	if _, present := headers["Stripe-Signature"]; present {
		t.Error("a header name reached the envelope in its canonical casing")
	}
	if got := headers["content-type"]; got != "application/json" {
		t.Errorf("headers[content-type] = %q, want %q", got, "application/json")
	}
}

// TestNewHTTPInput_DropsWhatAuthenticatesTheCaller is the refusal that is not an
// option. These headers prove who called; forwarding them would hand the author
// of the function the right to act as that caller, and write it to faasbox_logs
// along the way.
func TestNewHTTPInput_DropsWhatAuthenticatesTheCaller(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/invoke/echo", nil)
	// Each in a different casing: the rule is on the name, not on its spelling.
	r.Header.Set("X-API-Key", "fbx_secret")
	r.Header.Set("authorization", "Bearer secret")
	r.Header.Set("Cookie", "session=secret")
	r.Header.Set("Proxy-Authorization", "Basic secret")
	r.Header.Set("X-Kept", "visible")

	in := newHTTPInput(r, nil)
	headers := envelopeStrings(t, decodeEnvelope(t, in), "headers")

	for _, denied := range []string{"x-api-key", "authorization", "cookie", "proxy-authorization"} {
		if _, present := headers[denied]; present {
			t.Errorf("headers carries %q: a caller's credential must never reach a function", denied)
		}
	}
	if strings.Contains(in.Envelope, "secret") {
		t.Errorf("a credential survived somewhere in the envelope: %s", in.Envelope)
	}
	if got := headers["x-kept"]; got != "visible" {
		t.Errorf("headers[x-kept] = %q: the drop must not take the rest with it", got)
	}
}

// TestNewHTTPInput_JoinsRepeatedValues pins the rendering of a name given more
// than once, on both flat objects: one value, comma-joined, in the order
// received. That is HTTP's own semantics, and it is what keeps a function from
// having to handle two shapes for the same field.
func TestNewHTTPInput_JoinsRepeatedValues(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/invoke/echo?tag=first&tag=second", nil)
	r.Header.Add("X-Custom", "A")
	r.Header.Add("x-custom", "B")

	envelope := decodeEnvelope(t, newHTTPInput(r, nil))

	if got := envelopeStrings(t, envelope, "headers")["x-custom"]; got != "A, B" {
		t.Errorf("headers[x-custom] = %q, want %q", got, "A, B")
	}
	if got := envelopeStrings(t, envelope, "query")["tag"]; got != "first, second" {
		t.Errorf("query.tag = %q, want %q", got, "first, second")
	}
}

// TestNewHTTPInput_EmptyBodyAndQuery covers the two absences. A caller that sent
// no body sent an empty string — never "{}", which would be a document it never
// wrote — and a URL with no query string still carries the field, empty.
func TestNewHTTPInput_EmptyBodyAndQuery(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/invoke/echo", nil)
	envelope := decodeEnvelope(t, newHTTPInput(r, nil))

	if got := envelope["body"]; got != "" {
		t.Errorf("body = %v, want the empty string", got)
	}
	if got := envelopeStrings(t, envelope, "query"); len(got) != 0 {
		t.Errorf("query = %v, want an empty object", got)
	}
}

// TestInvokeHandler_EnvelopeReachesTheFunction drives the whole route: what the
// builder produced is what the subprocess reads on stdin, and what the log
// records. The function under test parses the envelope and answers with the
// fields, so the assertions read on the response rather than on an escaped blob.
func TestInvokeHandler_EnvelopeReachesTheFunction(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	key := createTestAPIKey(t, app, "envelope", []string{"*"})
	functionsDir, _ := setupTestFunctions(t, app, map[string]string{
		"echo": `const req = JSON.parse(await Bun.stdin.text());
console.log(JSON.stringify({
  trigger: req.trigger,
  method: req.method,
  path: req.path,
  tag: req.query.tag,
  signature: req.headers["stripe-signature"],
  body: req.body,
  credentials: ["x-api-key", "authorization", "cookie"].filter((h) => h in req.headers),
}));`,
	})

	scenario := tests.ApiScenario{
		Name:   "the function reads the call, not just the body",
		Method: http.MethodPost,
		URL:    "/invoke/echo?tag=first&tag=second",
		Headers: map[string]string{
			"X-API-Key":        key,
			"Authorization":    "Bearer nope",
			"Cookie":           "session=nope",
			"Stripe-Signature": "t=1,v1=abc",
		},
		Body:                  strings.NewReader(`{"id":"evt_1"}`),
		TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
		DisableTestAppCleanup: true,
		BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
			registerFaaSRoutes(app, e, functionsDir)
		},
		ExpectedStatus: 200,
		ExpectedContent: []string{
			`"trigger":"http"`,
			`"method":"POST"`,
			`"path":"/invoke/echo"`,
			`"tag":"first, second"`,
			`"signature":"t=1,v1=abc"`,
			`"body":"{\"id\":\"evt_1\"}"`,
			`"credentials":[]`,
		},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			// The log stores the envelope, not the body alone: that is what makes
			// a run readable afterwards — who called, how, and with what.
			entries := executionLogsOf(t, app, "echo")
			if len(entries) != 1 {
				t.Fatalf("got %d log entries, want 1", len(entries))
			}
			payload := decryptedJSON(app, entries[0], "requestPayload")
			var envelope map[string]any
			if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
				t.Fatalf("requestPayload = %s, want the envelope: %v", payload, err)
			}
			if envelope["trigger"] != "http" || envelope["body"] != `{"id":"evt_1"}` {
				t.Errorf("requestPayload = %s, want the envelope the function received", payload)
			}
			if strings.Contains(payload, "nope") {
				t.Errorf("a credential was persisted in the log: %s", payload)
			}
		},
	}
	scenario.Test(t)
}

// TestInvokeHandler_RefusesANonUTF8Body pins the refusal that replaces a silent
// corruption: a JSON envelope cannot carry arbitrary bytes, and encoding/json
// would swap the invalid sequences for U+FFFD without a word. Nothing runs.
func TestInvokeHandler_RefusesANonUTF8Body(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	key := createTestAPIKey(t, app, "binary", []string{"*"})
	functionsDir, _ := setupTestFunctions(t, app, map[string]string{"echo": ""})

	scenario := tests.ApiScenario{
		Name:                  "a body that is not valid UTF-8 is refused",
		Method:                http.MethodPost,
		URL:                   "/invoke/echo",
		Headers:               map[string]string{"X-API-Key": key},
		Body:                  bytes.NewReader([]byte{0xff, 0xfe, 0x00, 0x80}),
		TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
		DisableTestAppCleanup: true,
		BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
			registerFaaSRoutes(app, e, functionsDir)
		},
		ExpectedStatus:  400,
		ExpectedContent: []string{`valid UTF-8`},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			if got := len(executionLogsOf(t, app, "echo")); got != 0 {
				t.Errorf("execution logs = %d, want none: the refusal comes before the run", got)
			}
		},
	}
	scenario.Test(t)
}

// TestInvokeHandler_BodyCapCountsTheBodyAlone pins that FAASBOX_MAX_BODY_SIZE
// still bounds what the caller sent. The envelope built around it is ours, and
// counting it would make the documented limit depend on how many headers a
// client happens to send.
func TestInvokeHandler_BodyCapCountsTheBodyAlone(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	setupFaaSCollections(t, app)

	original := maxBodySize
	maxBodySize = 64
	t.Cleanup(func() { maxBodySize = original })

	key := createTestAPIKey(t, app, "sized", []string{"*"})
	functionsDir, _ := setupTestFunctions(t, app, map[string]string{
		"echo": `const req = JSON.parse(await Bun.stdin.text());
console.log(JSON.stringify({ size: req.body.length }));`,
	})

	// A body just under the cap, and a header far larger than what is left of it:
	// the envelope is well past 64 bytes and the call must still go through.
	body := `{"d":"` + strings.Repeat("x", 50) + `"}`
	scenario := tests.ApiScenario{
		Name:   "big headers do not eat the body budget",
		Method: http.MethodPost,
		URL:    "/invoke/echo",
		Headers: map[string]string{
			"X-API-Key": key,
			"X-Padding": strings.Repeat("p", 2048),
		},
		Body:                  strings.NewReader(body),
		TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
		DisableTestAppCleanup: true,
		BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
			registerFaaSRoutes(app, e, functionsDir)
		},
		ExpectedStatus:  200,
		ExpectedContent: []string{`"size":58`},
	}
	scenario.Test(t)
}

// TestRunFunction_CronEnvelope drives the scheduled path end to end: the job the
// scheduler registered is fired, and what the run received names the trigger
// that woke it.
func TestRunFunction_CronEnvelope(t *testing.T) {
	app, functionsDir, fn := startupApp(t)
	trigger := createTestTrigger(t, app, "nightly at 3", "0 3 * * *", fn.Id, true)
	setTriggerPayload(t, app, trigger.Id, `{"full":true}`)

	syncAllCronJobs(app, functionsDir, context.Background())
	fireCronJob(t, app, trigger.Id)

	entries := waitForExecutionLog(t, app, "booted", 1)
	if got := entries[0].GetString("trigger"); got != triggerCron {
		t.Errorf("trigger = %q, want %q", got, triggerCron)
	}

	envelope := loggedEnvelope(t, app, entries[0])
	if envelope["trigger"] != "cron" {
		t.Errorf("envelope trigger = %v, want \"cron\"", envelope["trigger"])
	}
	if envelope["triggerName"] != "nightly at 3" {
		t.Errorf("triggerName = %v, want the name the trigger carries", envelope["triggerName"])
	}
	if envelope["body"] != `{"full":true}` {
		t.Errorf("body = %v, want the payload as stored", envelope["body"])
	}
}

// TestRunFunction_TwoTriggersAreToldApart is what triggerName is for. Two
// schedules on one function, the same payload: before the envelope, nothing on
// stdin could tell which one had fired.
func TestRunFunction_TwoTriggersAreToldApart(t *testing.T) {
	app, functionsDir, fn := startupApp(t)
	morning := createTestTrigger(t, app, "morning sweep", "0 6 * * *", fn.Id, true)
	evening := createTestTrigger(t, app, "evening sweep", "0 18 * * *", fn.Id, true)

	syncAllCronJobs(app, functionsDir, context.Background())
	fireCronJob(t, app, morning.Id)
	fireCronJob(t, app, evening.Id)

	entries := waitForExecutionLog(t, app, "booted", 2)
	names := map[string]bool{}
	for _, entry := range entries {
		names[loggedEnvelope(t, app, entry)["triggerName"].(string)] = true
	}
	if !names["morning sweep"] || !names["evening sweep"] {
		t.Errorf("triggerNames seen = %v, want both schedules told apart", names)
	}
}

// TestScheduleStartupRuns_Envelope is the same for the other deadline, with the
// other absence: a trigger carrying no payload sends an empty JSON object, since
// "body" is always a string.
func TestScheduleStartupRuns_Envelope(t *testing.T) {
	app, functionsDir, fn := startupApp(t)
	createTestStartupTrigger(t, app, "boot warmup", 0, fn.Id, true)

	scheduleStartupRuns(context.Background(), app, functionsDir)

	entries := waitForExecutionLog(t, app, "booted", 1)
	if got := entries[0].GetString("trigger"); got != triggerStartup {
		t.Errorf("trigger = %q, want %q", got, triggerStartup)
	}

	envelope := loggedEnvelope(t, app, entries[0])
	if envelope["trigger"] != "startup" {
		t.Errorf("envelope trigger = %v, want \"startup\"", envelope["trigger"])
	}
	if envelope["triggerName"] != "boot warmup" {
		t.Errorf("triggerName = %v, want the name the trigger carries", envelope["triggerName"])
	}
	if envelope["body"] != "{}" {
		t.Errorf("body = %v, want the empty JSON object a missing payload reads as", envelope["body"])
	}
}

// TestMCPInvoke_Envelope covers the third path. No request is in sight, so the
// envelope names the trigger, carries the body and invents nothing — and the log
// records "mcp", the fourth value of a column whose values are closed.
func TestMCPInvoke_Envelope(t *testing.T) {
	app, functionsDir, _ := invokeMCPApp(t)
	session := mcpSession(t, app, functionsDir, unrestricted)

	var out mcpInvokeResult
	callToolOK(t, session, "invoke_function", map[string]any{
		"idOrName": "echo",
		"payload":  map[string]any{"hello": "world"},
	}, &out)

	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("result = %v, want the echoed envelope", out.Result)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(result["echo"].(string)), &envelope); err != nil {
		t.Fatalf("what reached stdin is not an envelope: %v", err)
	}
	if envelope["trigger"] != "mcp" {
		t.Errorf("envelope trigger = %v, want \"mcp\"", envelope["trigger"])
	}
	if envelope["body"] != `{"hello":"world"}` {
		t.Errorf("body = %v, want the payload as a string", envelope["body"])
	}
	for _, invented := range []string{"method", "path", "headers", "query", "triggerName"} {
		if _, present := envelope[invented]; present {
			t.Errorf("the envelope carries %q on a path with no request in sight", invented)
		}
	}

	entries := waitForExecutionLog(t, app, "echo", 1)
	if got := entries[0].GetString("trigger"); got != triggerMCP {
		t.Errorf("logged trigger = %q, want %q: the column has to accept it", got, triggerMCP)
	}
}

// loggedEnvelope reads back the envelope a run persisted in faasbox_logs.
func loggedEnvelope(t testing.TB, app core.App, entry *core.Record) map[string]any {
	t.Helper()
	payload := decryptedJSON(app, entry, "requestPayload")
	var envelope map[string]any
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		t.Fatalf("requestPayload = %s, want the envelope: %v", payload, err)
	}
	return envelope
}

// fireCronJob runs the scheduler job registered for a trigger record, the way a
// tick would — the alternative being a test that waits for the clock.
func fireCronJob(t testing.TB, app core.App, recordId string) {
	t.Helper()
	for _, job := range app.Cron().Jobs() {
		if job.Id() == cronJobPrefix+recordId {
			job.Run()
			return
		}
	}
	t.Fatalf("no cron job registered for trigger %q", recordId)
}

// setTriggerPayload writes the payload of an existing trigger record through
// app.Save, so the write is sealed the way any other one is.
func setTriggerPayload(t testing.TB, app core.App, recordId, payload string) {
	t.Helper()
	record, err := app.FindRecordById(faasboxTriggersCollection, recordId)
	if err != nil {
		t.Fatal(err)
	}
	record.Set("payload", payload)
	if err := app.Save(record); err != nil {
		t.Fatalf("failed to set the payload of trigger %q: %v", recordId, err)
	}
}

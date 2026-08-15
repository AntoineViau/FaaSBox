package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// What the encryption at rest has to hold, exercised through the hooks rather
// than through the primitives: crypto_test.go already covers the cipher itself.
//
// Every app here binds registerFieldEncryption, which a test app does not carry
// by default — that is what makes these the scenarios where a record is actually
// sealed, while the rest of the suite keeps readable fixtures.

// sealedApp is a test app carrying the four collections and the encryption
// hooks, in the order main.go binds them.
func sealedApp(t testing.TB) *tests.TestApp {
	t.Helper()

	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)

	setupFaaSCollections(t, app)
	setupFieldEncryption(t, app)
	return app
}

// storedColumn reads a column straight out of SQLite, going around the record
// layer entirely. It is what "unreadable in the database" has to mean: anything
// read through a record could be a decryption this code performed.
func storedColumn(t testing.TB, app core.App, collection, column, recordId string) string {
	t.Helper()
	var value string
	err := app.DB().NewQuery(
		"SELECT " + column + " FROM " + collection + " WHERE id = {:id}",
	).Bind(dbx.Params{"id": recordId}).Row(&value)
	if err != nil {
		t.Fatalf("failed to read %s.%s of %q: %v", collection, column, recordId, err)
	}
	return value
}

// saveSealedFunction stores a function on an app whose hooks seal it.
func saveSealedFunction(t testing.TB, app core.App, name, script, pkg string) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId(faasboxFunctionsCollection)
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(col)
	record.Set("name", name)
	record.Set("script", script)
	record.Set("packageJson", pkg)
	if err := app.Save(record); err != nil {
		t.Fatalf("failed to save the function %q: %v", name, err)
	}
	return record
}

// TestSealedField_SecondSaveDoesNotStackALayer is the main trap of writing these
// columns in place: the caller sends `script`, the hook seals `script`, and a
// hook that sealed unconditionally would seal its own output at the next save.
func TestSealedField_SecondSaveDoesNotStackALayer(t *testing.T) {
	app := sealedApp(t)
	const script = "console.log('once')"

	record := saveSealedFunction(t, app, "twice", script, "")
	first := storedColumn(t, app, faasboxFunctionsCollection, "script", record.Id)

	// A second save of the very same record, exactly what saving twice from the
	// editor without touching anything does.
	if err := app.Save(record); err != nil {
		t.Fatalf("the second save failed: %v", err)
	}
	second := storedColumn(t, app, faasboxFunctionsCollection, "script", record.Id)

	if second != first {
		t.Errorf("the second save rewrote the column (%q then %q), want it left alone", first, second)
	}
	if got := strings.Count(second, cipherPrefix); got != 1 {
		t.Errorf("the stored value carries %d version prefixes, want exactly 1", got)
	}

	stored, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := functionScript(app, stored); got != script {
		t.Errorf("script = %q, want %q — one layer, opened once", got, script)
	}
}

// TestSealedField_PlaintextLookingSealedIsStillSealed covers the collision the
// version prefix invites: a value that legitimately starts with `fbx1:`.
//
// Believing the prefix rather than verifying it would store such a value **in
// the clear** — the one property the scheme exists to establish — and, on a
// source column, nothing would open it on the way back: the accessor would yield
// the empty string and the disk sync would remove the file.
func TestSealedField_PlaintextLookingSealedIsStillSealed(t *testing.T) {
	app := sealedApp(t)
	functionsDir := t.TempDir()

	// Not valid TypeScript, and it does not need to be: what is under test is
	// what the column does with it, not what bun makes of it.
	const script = cipherPrefix + "this is not a ciphertext"
	record := saveSealedFunction(t, app, "lookalike", script, "")

	stored := storedColumn(t, app, faasboxFunctionsCollection, "script", record.Id)
	if stored == script {
		t.Fatalf("script = %q in the database, want it sealed rather than taken for a ciphertext", stored)
	}

	fresh, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := functionScript(app, fresh); got != script {
		t.Errorf("script reads back as %q, want %q", got, script)
	}

	// The half that would have been destructive: an unreadable script yields the
	// empty string, and an empty script removes its file.
	if err := syncRecordToDisk(app, fresh, functionsDir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(functionsDir, record.Id, "index.ts"))
	if err != nil {
		t.Fatalf("index.ts was not written: %v", err)
	}
	if string(data) != script {
		t.Errorf("index.ts = %q, want %q", data, script)
	}

	// And saving again still does not stack a layer: the value now stored really
	// is sealed, so the verified test recognises it.
	if err := app.Save(fresh); err != nil {
		t.Fatalf("the second save failed: %v", err)
	}
	if got := storedColumn(t, app, faasboxFunctionsCollection, "script", record.Id); got != stored {
		t.Errorf("the second save rewrote the column, want it left alone")
	}
}

// TestSealedJSON_PayloadLookingSealedIsStillSealed is the same collision on a
// JSON column, where the value under test is reachable without any trickery: a
// trigger payload set to the string "fbx1:…".
func TestSealedJSON_PayloadLookingSealedIsStillSealed(t *testing.T) {
	app := sealedApp(t)
	fn := saveSealedFunction(t, app, "lookalike-payload", "console.log('hi')", "")

	col, err := app.FindCollectionByNameOrId(faasboxCronJobsCollection)
	if err != nil {
		t.Fatal(err)
	}
	job := core.NewRecord(col)
	job.Set("name", "decoy")
	job.Set("schedule", "* * * * *")
	job.Set("function", fn.Id)
	job.Set("active", true)
	job.Set("payload", cipherPrefix+"not a ciphertext either")
	if err := app.Save(job); err != nil {
		t.Fatalf("failed to save the trigger: %v", err)
	}

	stored := storedColumn(t, app, faasboxCronJobsCollection, "payload", job.Id)
	if strings.Contains(stored, "not a ciphertext") {
		t.Fatalf("payload = %q in the database, want it sealed", stored)
	}

	fresh, err := app.FindRecordById(faasboxCronJobsCollection, job.Id)
	if err != nil {
		t.Fatal(err)
	}
	var payload string
	if err := json.Unmarshal([]byte(cronPayloadText(app, fresh)), &payload); err != nil {
		t.Fatalf("the payload does not read back as a JSON string: %v", err)
	}
	if want := cipherPrefix + "not a ciphertext either"; payload != want {
		t.Errorf("payload reads back as %q, want %q", payload, want)
	}
}

// TestSealedField_EmptyStaysEmpty covers the rule three behaviours of the
// product rest on: an empty field is a fact, and sealing it would turn it into a
// value. The visible consequence is on disk, where an empty artefact removes its
// file rather than writing one.
func TestSealedField_EmptyStaysEmpty(t *testing.T) {
	app := sealedApp(t)
	functionsDir := t.TempDir()

	record := saveSealedFunction(t, app, "no-manifest", "console.log('hi')", "")

	if got := storedColumn(t, app, faasboxFunctionsCollection, "packageJson", record.Id); got != "" {
		t.Errorf("packageJson = %q in the database, want it left empty", got)
	}

	if err := syncRecordToDisk(app, record, functionsDir); err != nil {
		t.Fatal(err)
	}
	pkgPath := filepath.Join(functionsDir, record.Id, "package.json")
	if _, err := os.Stat(pkgPath); !os.IsNotExist(err) {
		t.Errorf("package.json was written for an empty field (stat error: %v)", err)
	}
	// The script, which is not empty, is on disk in the clear: what bun compiles
	// is the plaintext, never the stored value.
	data, err := os.ReadFile(filepath.Join(functionsDir, record.Id, "index.ts"))
	if err != nil {
		t.Fatalf("index.ts not written: %v", err)
	}
	if string(data) != "console.log('hi')" {
		t.Errorf("index.ts = %q, want the plaintext script", data)
	}
}

// TestSealedLog_OutputUnreadableInTheDatabaseReadableByTheAPI is the point of the
// whole exercise, on the collection that carries the most: a replica of the
// database yields nothing, the API yields the run.
func TestSealedLog_OutputUnreadableInTheDatabaseReadableByTheAPI(t *testing.T) {
	app := sealedApp(t)
	functionsDir := t.TempDir()
	fn := saveSealedFunction(t, app, "chatty", "console.log('hi')", "")
	if err := syncRecordToDisk(app, fn, functionsDir); err != nil {
		t.Fatal(err)
	}

	const secret = "token-abcdef0123456789"
	recordExecution(app, logEntry{
		FunctionId:     fn.Id,
		FunctionName:   "chatty",
		Trigger:        "http",
		Status:         "success",
		Stdout:         `{"leaked":"` + secret + `"}`,
		Stderr:         "warning: " + secret,
		RequestPayload: `{"input":"` + secret + `"}`,
	})

	entries := executionLogsOf(t, app, "chatty")
	if len(entries) != 1 {
		t.Fatalf("got %d log entries, want 1", len(entries))
	}
	entry := entries[0]

	for _, column := range []string{"functionName", "stdout", "stderr", "requestPayload"} {
		stored := storedColumn(t, app, faasboxLogsCollection, column, entry.Id)
		if strings.Contains(stored, secret) || strings.Contains(stored, "chatty") {
			t.Errorf("%s is legible in the database: %q", column, stored)
		}
		if !strings.Contains(stored, cipherPrefix) {
			t.Errorf("%s = %q, want a sealed value", column, stored)
		}
	}

	contract := logContractOf(app, entry)
	if contract.FunctionName != "chatty" {
		t.Errorf("functionName = %q, want %q", contract.FunctionName, "chatty")
	}
	if !strings.Contains(contract.Stdout, secret) {
		t.Errorf("stdout = %q, want the output the run produced", contract.Stdout)
	}
	if !strings.Contains(contract.Stderr, secret) {
		t.Errorf("stderr = %q, want the trace the run produced", contract.Stderr)
	}
}

// TestSealedLog_PayloadComesBackAsADocument guards the failure that would not
// announce itself. A sealed value has exactly the shape a payload cut at the
// storage cap legitimately takes — an escaped string rather than an object — so
// a relay that did not open the column first would publish the ciphertext as a
// truncated payload, which the API documents as normal.
func TestSealedLog_PayloadComesBackAsADocument(t *testing.T) {
	app := sealedApp(t)
	fn := saveSealedFunction(t, app, "payloads", "console.log('hi')", "")

	t.Run("a document stays a document", func(t *testing.T) {
		recordExecution(app, logEntry{
			FunctionId:     fn.Id,
			FunctionName:   "payloads",
			Trigger:        "http",
			Status:         "success",
			RequestPayload: `{"hello":"world"}`,
		})
		entry := executionLogsOf(t, app, "payloads")[0]

		payload := rawPayload(app, entry)
		var document map[string]string
		if err := json.Unmarshal(payload, &document); err != nil {
			t.Fatalf("requestPayload = %s, want a JSON document: %v", payload, err)
		}
		if document["hello"] != "world" {
			t.Errorf("requestPayload = %s, want the document the caller sent", payload)
		}
	})

	t.Run("a truncated payload stays an escaped string", func(t *testing.T) {
		app := sealedApp(t)
		fn := saveSealedFunction(t, app, "cut", "console.log('hi')", "")
		recordExecution(app, logEntry{
			FunctionId:     fn.Id,
			FunctionName:   "cut",
			Trigger:        "http",
			Status:         "success",
			RequestPayload: `{"data":"` + strings.Repeat("p", maxLoggedPayload) + `"}`,
		})
		entry := executionLogsOf(t, app, "cut")[0]

		var text string
		if err := json.Unmarshal(rawPayload(app, entry), &text); err != nil {
			t.Fatalf("a truncated payload must come back as a JSON string: %v", err)
		}
		if !strings.Contains(text, "truncated") {
			t.Errorf("requestPayload = %q, want the truncation marker", text)
		}
	})
}

// TestSealedDeps_ColumnsUnreadableAfterAnInstall covers the two writes no hook
// ever sees: they go around app.Save on purpose, so they seal by hand or not at
// all. depsError is the column that carries the output of bun install, which can
// print the URL of a private registry, token included.
func TestSealedDeps_ColumnsUnreadableAfterAnInstall(t *testing.T) {
	app := sealedApp(t)
	functionsDir := t.TempDir()

	// A bun that writes a lockfile naming a registry, and says so on the way out.
	fakeBun(t, `mkdir -p node_modules
echo 'resolved from https://registry.internal/?token=s3cr3t' > bun.lock
echo 'reading https://registry.internal/?token=s3cr3t' >&2`)

	record := saveSealedFunction(t, app, "installer", "console.log('hi')",
		`{"dependencies":{"dayjs":"^1.11.0"}}`)
	if err := syncRecordToDisk(app, record, functionsDir); err != nil {
		t.Fatal(err)
	}

	scheduleDepsInstall(context.Background(), app, record, functionsDir)
	waitDepsStatus(t, app, record.Id, depsStatusReady)

	lock := storedColumn(t, app, faasboxFunctionsCollection, "bunLock", record.Id)
	if strings.Contains(lock, "s3cr3t") || !strings.HasPrefix(lock, cipherPrefix) {
		t.Errorf("bunLock = %q, want a sealed value", lock)
	}

	// The same write path, on the column that carries a failure.
	setDepsState(app, record.Id, "installer", depsStatusError,
		"error: https://registry.internal/?token=s3cr3t refused")
	stored := storedColumn(t, app, faasboxFunctionsCollection, "depsError", record.Id)
	if strings.Contains(stored, "s3cr3t") || !strings.HasPrefix(stored, cipherPrefix) {
		t.Errorf("depsError = %q, want a sealed value", stored)
	}
	// depsStatus stays in the clear: it is filtered, and it says nothing.
	if got := storedColumn(t, app, faasboxFunctionsCollection, "depsStatus", record.Id); got != depsStatusError {
		t.Errorf("depsStatus = %q, want it stored in the clear", got)
	}

	fresh, err := app.FindRecordById(faasboxFunctionsCollection, record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(functionDepsError(app, fresh), "refused") {
		t.Error("the dependency error does not read back")
	}
}

// TestSealedFunction_CollectionAPIRendersThePlaintext is what holds the editor
// up: it reads the collections API directly, so the enrichment is the only thing
// between a sealed column and an unusable screen.
func TestSealedFunction_CollectionAPIRendersThePlaintext(t *testing.T) {
	app := sealedApp(t)
	record := saveSealedFunction(t, app, "rendered", "console.log('visible')", `{"name":"x"}`)

	scenario := tests.ApiScenario{
		Name:                  "a superuser reads a function back in the clear",
		Method:                http.MethodGet,
		URL:                   "/api/collections/" + faasboxFunctionsCollection + "/records/" + record.Id,
		Headers:               map[string]string{"Authorization": superuserToken},
		TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
		DisableTestAppCleanup: true,
		ExpectedStatus:        200,
		ExpectedContent:       []string{`"name":"rendered"`, `console.log('visible')`, `{\"name\":\"x\"}`},
		// env keeps its dedicated route and is never opened by the enrichment.
		NotExpectedContent: []string{cipherPrefix},
	}
	scenario.Test(t)
}

// TestSealedAPIKey_StaysUsable pins that sealing the two legible columns of a key
// leaves the credential itself alone: what a presented key is matched on is
// keyHash, which is not encrypted and never will be.
func TestSealedAPIKey_StaysUsable(t *testing.T) {
	app := sealedApp(t)

	functionsDir, functions := setupTestFunctions(t, app, map[string]string{"echo": ""})
	fn := functions["echo"]
	rawKey := createTestAPIKey(t, app, "sealed label", []string{fn.Id})

	stored, err := app.FindFirstRecordByData(faasboxAPIKeysCollection, "keyHash", hashAPIKey(rawKey))
	if err != nil {
		t.Fatalf("the key was not stored: %v", err)
	}
	for _, column := range []string{"name", "keyPrefix"} {
		if got := storedColumn(t, app, faasboxAPIKeysCollection, column, stored.Id); !strings.HasPrefix(got, cipherPrefix) {
			t.Errorf("%s = %q, want a sealed value", column, got)
		}
	}
	if got := storedColumn(t, app, faasboxAPIKeysCollection, "keyHash", stored.Id); got != hashAPIKey(rawKey) {
		t.Errorf("keyHash = %q, want the hash left as it stands", got)
	}
	if got := apiKeyName(app, stored); got != "sealed label" {
		t.Errorf("the key label reads back as %q, want %q", got, "sealed label")
	}

	scenario := tests.ApiScenario{
		Name:                  "a key whose label is sealed still invokes",
		Method:                http.MethodPost,
		URL:                   "/invoke/echo",
		Body:                  strings.NewReader(`{"hello":"world"}`),
		Headers:               map[string]string{"X-API-Key": rawKey},
		TestAppFactory:        func(t testing.TB) *tests.TestApp { return app },
		DisableTestAppCleanup: true,
		BeforeTestFunc: func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
			registerFaaSRoutes(app, e, functionsDir)
		},
		ExpectedStatus:  200,
		ExpectedContent: []string{`"result"`},
	}
	scenario.Test(t)
}

// TestSealedCron_MissedRunsAreStillDetected covers the reader that would have
// been missed: the startup pass parses the expression itself, outside the
// scheduler, and a sealed expression handed to the cron parser detects nothing.
func TestSealedCron_MissedRunsAreStillDetected(t *testing.T) {
	app := sealedApp(t)
	now := time.Now()

	fn := saveSealedFunction(t, app, "scheduled", "console.log('hi')", "")
	job := createTestCronJob(t, app, "every minute", "* * * * *", fn.Id, true)
	setCronJobDate(t, app, job.Id, "lastRunAt", now.Add(-10*time.Minute))

	if got := storedColumn(t, app, faasboxCronJobsCollection, "schedule", job.Id); !strings.HasPrefix(got, cipherPrefix) {
		t.Fatalf("schedule = %q, want a sealed value — the test would prove nothing", got)
	}

	reportMissedCronRuns(app, now)

	entries := missedLogs(t, app)
	if len(entries) != 1 {
		t.Fatalf("got %d missed entries, want 1", len(entries))
	}
	if got := decryptedText(app, entries[0], "stderr"); !strings.Contains(got, "missed") {
		t.Errorf("the missed entry says %q, want it to describe the occurrences", got)
	}
}

// TestSealedOAuth_AuthorizationFlowCompletes walks the whole journey on an
// instance whose client and grant are sealed. Five comparisons ride on those
// columns and every one of them fails by refusing, never by reporting: the flow
// completing is the only thing that proves them right.
func TestSealedOAuth_AuthorizationFlowCompletes(t *testing.T) {
	app := sealedApp(t)
	if err := ensureOAuthClientsCollection(app); err != nil {
		t.Fatal(err)
	}
	if err := ensureOAuthGrantsCollection(app); err != nil {
		t.Fatal(err)
	}

	step := func(t *testing.T, s tests.ApiScenario) {
		s.TestAppFactory = func(t testing.TB) *tests.TestApp { return app }
		s.DisableTestAppCleanup = true
		s.BeforeTestFunc = func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
			if err := mountOAuth(e, testOAuthConfig); err != nil {
				t.Fatalf("failed to mount the OAuth routes: %v", err)
			}
		}
		s.Test(t)
	}

	// 1. The registration, which is what seals a name and a list of URLs.
	var clientId string
	step(t, tests.ApiScenario{
		Name:   "register",
		Method: http.MethodPost,
		URL:    "/oauth/register",
		Body: strings.NewReader(`{"client_name":"sealed agent","redirect_uris":["` +
			testRedirectURI + `"]}`),
		ExpectedStatus:  201,
		ExpectedContent: []string{`"client_id"`},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			var body struct {
				ClientID string `json:"client_id"`
			}
			if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			clientId = body.ClientID

			// Found by fingerprint: the identifier itself is sealed, so no
			// equality on the column could ever hold.
			client, err := findOAuthClient(app, clientId)
			if err != nil {
				t.Fatalf("the registration was not stored: %v", err)
			}
			for _, column := range []string{"clientId", "name", "redirectUris"} {
				got := storedColumn(t, app, faasboxOAuthClientsCollection, column, client.Id)
				if !strings.Contains(got, cipherPrefix) {
					t.Errorf("%s = %q, want a sealed value", column, got)
				}
			}
			if got := storedColumn(t, app, faasboxOAuthClientsCollection, "clientIdHash", client.Id); got != blindIndex(clientId) {
				t.Errorf("clientIdHash = %q, want the fingerprint of the identifier", got)
			}
			if got := clientDisplayName(app, client); got != "sealed agent" {
				t.Errorf("the client reads back as %q, want %q", got, "sealed agent")
			}
		},
	})

	// 2. The authorization request. This is the step a sealed redirectUris kills
	// outright: the comparison is against the registered list.
	var grantId string
	step(t, tests.ApiScenario{
		Name:           "authorize",
		Method:         http.MethodGet,
		URL:            authorizeURL(clientId, nil),
		ExpectedStatus: 302,
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			location := res.Header.Get("Location")
			if !strings.HasPrefix(location, "/consent?request=") {
				t.Fatalf("Location = %q, want the consent page", location)
			}
			grantId = strings.TrimPrefix(location, "/consent?request=")

			for _, column := range []string{"redirectUri", "codeChallenge", "resource", "state"} {
				got := storedColumn(t, app, faasboxOAuthGrantsCollection, column, grantId)
				if !strings.HasPrefix(got, cipherPrefix) {
					t.Errorf("%s = %q, want a sealed value", column, got)
				}
			}
		},
	})

	// 3. The click, which mints the code and echoes the state back.
	var code string
	step(t, tests.ApiScenario{
		Name:            "consent",
		Method:          http.MethodPost,
		URL:             "/api/faasbox/oauth/requests/" + grantId + "/decision",
		Body:            strings.NewReader(`{"approve":true}`),
		Headers:         map[string]string{"Authorization": superuserToken},
		ExpectedStatus:  200,
		ExpectedContent: []string{`"redirectUrl"`},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			var body struct {
				RedirectURL string `json:"redirectUrl"`
			}
			if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			parsed, err := url.Parse(body.RedirectURL)
			if err != nil {
				t.Fatal(err)
			}
			code = parsed.Query().Get("code")
			if code == "" {
				t.Fatalf("redirectUrl = %q, want it to carry a code", body.RedirectURL)
			}
			// The state is what the client matches its own request on, so it has
			// to come back as the client wrote it.
			if got := parsed.Query().Get("state"); got != testState {
				t.Errorf("state = %q, want %q", got, testState)
			}
		},
	})

	// 4. The exchange, which weighs the redirect URI, the audience and PKCE — all
	// three read off sealed columns.
	body, headers := tokenForm(url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {clientId},
		"redirect_uri":  {testRedirectURI},
		"code_verifier": {testCodeVerifier},
		"resource":      {testOAuthConfig.Resource},
	})
	step(t, tests.ApiScenario{
		Name:            "token",
		Method:          http.MethodPost,
		URL:             "/oauth/token",
		Body:            body,
		Headers:         headers,
		ExpectedStatus:  200,
		ExpectedContent: []string{`"access_token"`, `"refresh_token"`},
	})
}

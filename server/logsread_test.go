package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// logsApp prepares an app with the four collections and one function named
// "noisy", which is what the log route is read through.
func logsApp(t testing.TB) (*tests.TestApp, string, *core.Record) {
	t.Helper()

	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)

	setupFaaSCollections(t, app)
	bindFunctionNameHook(app)

	functionsDir, functions := setupTestFunctions(t, app, map[string]string{"noisy": ""})
	return app, functionsDir, functions["noisy"]
}

// logsScenario wires a scenario onto an already prepared app.
func logsScenario(app *tests.TestApp, functionsDir string, s tests.ApiScenario) tests.ApiScenario {
	s.TestAppFactory = func(t testing.TB) *tests.TestApp { return app }
	s.DisableTestAppCleanup = true
	s.BeforeTestFunc = func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
		registerFaaSRoutes(app, e, functionsDir)
	}
	return s
}

// seedLog writes one faasbox_logs entry with its id and its created date
// chosen. recordExecution cannot do either: created is an autodate field, so
// entries written in a loop all land on the same instant, and an ordering test
// needs them apart.
func seedLog(t testing.TB, app core.App, id string, entry logEntry, created time.Time) *core.Record {
	t.Helper()

	col, err := app.FindCollectionByNameOrId(faasboxLogsCollection)
	if err != nil {
		t.Fatal(err)
	}

	record := core.NewRecord(col)
	record.Id = id
	record.Set("function", entry.FunctionId)
	record.Set("functionName", entry.FunctionName)
	record.Set("trigger", entry.Trigger)
	record.Set("status", entry.Status)
	record.Set("duration", entry.DurationMs)
	record.Set("stdout", entry.Stdout)
	record.Set("stderr", entry.Stderr)
	// Left unset when empty, exactly as recordExecution does: that is the state
	// an entry without a payload is actually stored in.
	if entry.RequestPayload != "" {
		record.Set("requestPayload", entry.RequestPayload)
	}
	record.Set("exitCode", entry.ExitCode)
	if err := app.Save(record); err != nil {
		t.Fatalf("failed to seed the log entry %q: %v", id, err)
	}

	setRecordDate(t, app, faasboxLogsCollection, record.Id, "created", created)
	return record
}

// seedLogs writes count entries for a function, one minute apart, oldest first.
// The stdout of each names its rank so an ordering assertion can read it back.
func seedLogs(t testing.TB, app core.App, fn *core.Record, count int) {
	t.Helper()

	base := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	for i := range count {
		seedLog(t, app, fmt.Sprintf("logentry%07d", i), logEntry{
			FunctionId:   fn.Id,
			FunctionName: fn.GetString("name"),
			Trigger:      "http",
			Status:       "success",
			DurationMs:   int64(i),
			Stdout:       fmt.Sprintf(`{"n":%d}`, i),
			Stderr:       "diag",
		}, base.Add(time.Duration(i)*time.Minute))
	}
}

// logsBody decodes the envelope the route answers with.
type logsBody struct {
	Logs  []map[string]any `json:"logs"`
	Count int              `json:"count"`
}

// decodeLogs reads the response body of the log route.
func decodeLogs(t testing.TB, res *http.Response) logsBody {
	t.Helper()
	var body logsBody
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode the log response: %v", err)
	}
	return body
}

// --- Contract ---

func TestFunctionLogsHandler_Contract(t *testing.T) {
	t.Run("publishes exactly the eleven keys of the contract", func(t *testing.T) {
		app, dir, fn := logsApp(t)
		seedLog(t, app, "logentrycontrac", logEntry{
			FunctionId:     fn.Id,
			FunctionName:   "noisy",
			Trigger:        "http",
			Status:         "success",
			DurationMs:     42,
			Stdout:         `{"ok":true}`,
			Stderr:         "diag",
			RequestPayload: `{"n":3}`,
			ExitCode:       0,
		}, time.Date(2026, 8, 9, 10, 12, 31, 0, time.UTC))

		s := logsScenario(app, dir, tests.ApiScenario{
			Name:           "read the logs",
			Method:         http.MethodGet,
			URL:            "/api/faasbox/functions/noisy/logs",
			Headers:        manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus: 200,
			ExpectedContent: []string{
				`"count":1`,
				`"functionName":"noisy"`,
				`"durationMs":42`,
				`"stderr":"diag"`,
				`"requestPayload":{"n":3}`,
				`"created":"2026-08-09T10:12:31Z"`,
			},
			// The route is already per function, and the internal columns of the
			// record are not contract.
			NotExpectedContent: []string{`"function"`, `"duration"`, `"updated"`, `"collectionName"`},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				body := decodeLogs(t, res)
				if len(body.Logs) != 1 {
					t.Fatalf("got %d entries, want 1", len(body.Logs))
				}

				want := []string{
					"created", "durationMs", "exitCode", "functionName", "id",
					"requestPayload", "status", "stderr", "stdout", "trigger", "truncated",
				}
				got := make([]string, 0, len(body.Logs[0]))
				for key := range body.Logs[0] {
					got = append(got, key)
				}
				slices.Sort(got)
				if !slices.Equal(got, want) {
					t.Errorf("contract keys = %v, want %v", got, want)
				}
			},
		})
		s.Test(t)
	})

	t.Run("renders no payload as null, never as an empty string", func(t *testing.T) {
		app, dir, fn := logsApp(t)
		seedLog(t, app, "logentrynopaylo", logEntry{
			FunctionId:   fn.Id,
			FunctionName: "noisy",
			Trigger:      "cron",
			Status:       "success",
			Stdout:       `{"ok":true}`,
		}, time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC))

		s := logsScenario(app, dir, tests.ApiScenario{
			Name:            "cron entry without payload",
			Method:          http.MethodGet,
			URL:             "/api/faasbox/functions/noisy/logs",
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  200,
			ExpectedContent: []string{`"requestPayload":null`, `"trigger":"cron"`},
		})
		s.Test(t)
	})

	t.Run("answers an empty list for a function that never ran", func(t *testing.T) {
		app, dir, _ := logsApp(t)
		s := logsScenario(app, dir, tests.ApiScenario{
			Name:            "no entry",
			Method:          http.MethodGet,
			URL:             "/api/faasbox/functions/noisy/logs",
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  200,
			ExpectedContent: []string{`"logs":[]`, `"count":0`},
		})
		s.Test(t)
	})
}

// --- Ordering, filter and bound ---

func TestFunctionLogsHandler_Ordering(t *testing.T) {
	app, dir, fn := logsApp(t)
	seedLogs(t, app, fn, 3)

	s := logsScenario(app, dir, tests.ApiScenario{
		Name:            "most recent first",
		Method:          http.MethodGet,
		URL:             "/api/faasbox/functions/noisy/logs",
		Headers:         manageKeyHeader(t, app, "manager", nil),
		ExpectedStatus:  200,
		ExpectedContent: []string{`"count":3`},
		AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
			body := decodeLogs(t, res)
			if body.Count != 3 || len(body.Logs) != 3 {
				t.Fatalf("got count=%d and %d entries, want 3 of each", body.Count, len(body.Logs))
			}
			want := []string{`{"n":2}`, `{"n":1}`, `{"n":0}`}
			for i, expected := range want {
				if got := body.Logs[i]["stdout"]; got != expected {
					t.Errorf("entry %d stdout = %v, want %v", i, got, expected)
				}
			}
		},
	})
	s.Test(t)
}

func TestFunctionLogsHandler_FiltersOnTheRelation(t *testing.T) {
	t.Run("returns only the entries of the function in the path", func(t *testing.T) {
		app, dir, fn := logsApp(t)
		other := saveTestFunction(t, app, dir, "quiet-one", defaultTestScript, "")
		seedLogs(t, app, fn, 2)
		seedLog(t, app, "logentryotherfn", logEntry{
			FunctionId:   other.Id,
			FunctionName: "quiet-one",
			Trigger:      "http",
			Status:       "success",
			Stdout:       `{"other":true}`,
		}, time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC))

		s := logsScenario(app, dir, tests.ApiScenario{
			Name:               "one function only",
			Method:             http.MethodGet,
			URL:                "/api/faasbox/functions/noisy/logs",
			Headers:            manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:     200,
			ExpectedContent:    []string{`"count":2`},
			NotExpectedContent: []string{`"other":true`},
		})
		s.Test(t)
	})

	t.Run("keeps the history across a rename", func(t *testing.T) {
		app, dir, fn := logsApp(t)
		seedLogs(t, app, fn, 2)

		fn.Set("name", "quiet")
		if err := app.Save(fn); err != nil {
			t.Fatalf("failed to rename the function: %v", err)
		}

		s := logsScenario(app, dir, tests.ApiScenario{
			Name:           "read under the new name",
			Method:         http.MethodGet,
			URL:            "/api/faasbox/functions/quiet/logs",
			Headers:        manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus: 200,
			// The relation carries the history; functionName is the trace of what
			// the function was called when the entry was written, and stays put.
			ExpectedContent: []string{`"count":2`, `"functionName":"noisy"`},
		})
		s.Test(t)
	})
}

func TestFunctionLogsHandler_Limit(t *testing.T) {
	app, dir, fn := logsApp(t)
	seedLogs(t, app, fn, maxLogsLimit+5)

	cases := []struct {
		name  string
		query string
		count int
	}{
		{"absent means fifty", "", defaultLogsLimit},
		{"empty means fifty", "?limit=", defaultLogsLimit},
		{"a value below the ceiling is honoured", "?limit=3", 3},
		{"the ceiling itself passes", "?limit=200", maxLogsLimit},
		{"beyond the ceiling is capped", "?limit=1000", maxLogsLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := logsScenario(app, dir, tests.ApiScenario{
				Name:            tc.name,
				Method:          http.MethodGet,
				URL:             "/api/faasbox/functions/noisy/logs" + tc.query,
				Headers:         manageKeyHeader(t, app, "reader", nil),
				ExpectedStatus:  200,
				ExpectedContent: []string{fmt.Sprintf(`"count":%d`, tc.count)},
				AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
					body := decodeLogs(t, res)
					if body.Count != tc.count || len(body.Logs) != tc.count {
						t.Errorf("got count=%d and %d entries, want %d of each",
							body.Count, len(body.Logs), tc.count)
					}
				},
			})
			s.Test(t)
		})
	}

	// A limit that cannot be read is refused rather than dropped: answering the
	// default would return something other than what was asked for.
	// "%205" is a leading space: Atoi refuses it, and so does the route.
	for _, raw := range []string{"abc", "0", "-3", "1.5", "%205"} {
		t.Run("refuses limit="+raw, func(t *testing.T) {
			s := logsScenario(app, dir, tests.ApiScenario{
				Name:            "unreadable limit",
				Method:          http.MethodGet,
				URL:             "/api/faasbox/functions/noisy/logs?limit=" + raw,
				Headers:         manageKeyHeader(t, app, "reader", nil),
				ExpectedStatus:  400,
				ExpectedContent: []string{`"limit\" must be a whole number`},
			})
			s.Test(t)
		})
	}
}

// --- Authorisation and lookup ---

func TestFunctionLogsHandler_Authorisation(t *testing.T) {
	t.Run("refuses a valid key without canManage", func(t *testing.T) {
		app, dir, fn := logsApp(t)
		seedLogs(t, app, fn, 1)
		s := logsScenario(app, dir, tests.ApiScenario{
			Name:            "invoke-only key",
			Method:          http.MethodGet,
			URL:             "/api/faasbox/functions/noisy/logs",
			Headers:         map[string]string{"X-API-Key": createTestAPIKey(t, app, "invoke-only", nil)},
			ExpectedStatus:  403,
			ExpectedContent: []string{`not authorized to manage functions`},
		})
		s.Test(t)
	})

	t.Run("refuses a key scoped on another function", func(t *testing.T) {
		app, dir, fn := logsApp(t)
		seedLogs(t, app, fn, 1)
		other := saveTestFunction(t, app, dir, "other-func", defaultTestScript, "")
		s := logsScenario(app, dir, tests.ApiScenario{
			Name:               "scope elsewhere",
			Method:             http.MethodGet,
			URL:                "/api/faasbox/functions/noisy/logs",
			Headers:            manageKeyHeader(t, app, "scoped-elsewhere", []string{other.Id}),
			ExpectedStatus:     403,
			ExpectedContent:    []string{`API key is not authorized for function`},
			NotExpectedContent: []string{`"logs"`},
		})
		s.Test(t)
	})

	t.Run("lets a key scoped on this function through", func(t *testing.T) {
		app, dir, fn := logsApp(t)
		seedLogs(t, app, fn, 1)
		s := logsScenario(app, dir, tests.ApiScenario{
			Name:            "scope covers it",
			Method:          http.MethodGet,
			URL:             "/api/faasbox/functions/noisy/logs",
			Headers:         manageKeyHeader(t, app, "scoped", []string{fn.Id}),
			ExpectedStatus:  200,
			ExpectedContent: []string{`"count":1`},
		})
		s.Test(t)
	})

	t.Run("refuses a request without a key", func(t *testing.T) {
		app, dir, _ := logsApp(t)
		s := logsScenario(app, dir, tests.ApiScenario{
			Name:            "no header",
			Method:          http.MethodGet,
			URL:             "/api/faasbox/functions/noisy/logs",
			ExpectedStatus:  401,
			ExpectedContent: []string{`Missing X-API-Key header`},
		})
		s.Test(t)
	})

	t.Run("lets a superuser through without a key", func(t *testing.T) {
		app, dir, fn := logsApp(t)
		seedLogs(t, app, fn, 1)
		s := logsScenario(app, dir, tests.ApiScenario{
			Name:            "superuser read",
			Method:          http.MethodGet,
			URL:             "/api/faasbox/functions/noisy/logs",
			Headers:         map[string]string{"Authorization": superuserToken},
			ExpectedStatus:  200,
			ExpectedContent: []string{`"count":1`},
		})
		s.Test(t)
	})
}

func TestFunctionLogsHandler_Lookup(t *testing.T) {
	t.Run("answers 404 for a segment designating nothing", func(t *testing.T) {
		app, dir, _ := logsApp(t)
		s := logsScenario(app, dir, tests.ApiScenario{
			Name:            "unknown function",
			Method:          http.MethodGet,
			URL:             "/api/faasbox/functions/inexistante/logs",
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  404,
			ExpectedContent: []string{`Function not found`},
		})
		s.Test(t)
	})

	t.Run("answers 400 for a malformed segment", func(t *testing.T) {
		app, dir, _ := logsApp(t)
		s := logsScenario(app, dir, tests.ApiScenario{
			Name:            "malformed segment",
			Method:          http.MethodGet,
			URL:             "/api/faasbox/functions/nom_invalide/logs",
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  400,
			ExpectedContent: []string{`Invalid function name`},
		})
		s.Test(t)
	})

	t.Run("reads by id as well as by name", func(t *testing.T) {
		app, dir, fn := logsApp(t)
		seedLogs(t, app, fn, 1)
		s := logsScenario(app, dir, tests.ApiScenario{
			Name:            "read by id",
			Method:          http.MethodGet,
			URL:             "/api/faasbox/functions/" + fn.Id + "/logs",
			Headers:         manageKeyHeader(t, app, "manager", nil),
			ExpectedStatus:  200,
			ExpectedContent: []string{`"count":1`},
		})
		s.Test(t)
	})
}

// --- Unit: the bound itself ---

func TestLogsLimit(t *testing.T) {
	cases := []struct {
		raw     string
		want    int
		refused bool
	}{
		{"", defaultLogsLimit, false},
		{"1", 1, false},
		{"50", 50, false},
		{"200", maxLogsLimit, false},
		{"201", maxLogsLimit, false},
		{"1000000", maxLogsLimit, false},
		{"0", 0, true},
		{"-1", 0, true},
		{"abc", 0, true},
		{"", defaultLogsLimit, false},
	}

	for _, tc := range cases {
		got, err := logsLimit(tc.raw)
		if tc.refused {
			if err == nil {
				t.Errorf("logsLimit(%q) = %d, want a refusal", tc.raw, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("logsLimit(%q) refused: %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("logsLimit(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

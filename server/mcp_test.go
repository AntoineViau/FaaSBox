package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// The tools are exercised through a real MCP session over an in-memory
// transport, not by calling their handlers. That is what pins the half a direct
// call cannot see: the input schema the SDK infers, the validation it runs
// before the handler, and the shape of the result on the wire.
//
// The endpoint itself is exercised over HTTP, because what it has to prove is
// the middleware chain — a request with no key, and a key without canManage.

// mcpSession connects a client to a server built with the given scope, and
// returns the client's end. Everything is closed when the test ends.
func mcpSession(t testing.TB, app core.App, functionsDir string, allowed []string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	server := newMCPServer(app, functionsDir, allowed, nil)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("failed to connect the MCP server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "faasbox-test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("failed to connect the MCP client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// callTool calls a tool and hands back the result plus its text content, which
// is the serialized JSON of the structured output.
func callTool(t testing.TB, session *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}

	var text strings.Builder
	for _, content := range res.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	return res, text.String()
}

// callToolOK calls a tool, fails the test if it reported an error, and decodes
// its output into v.
func callToolOK(t testing.TB, session *mcp.ClientSession, name string, args map[string]any, v any) {
	t.Helper()
	res, text := callTool(t, session, name, args)
	if res.IsError {
		t.Fatalf("%s reported an error: %s", name, text)
	}
	if v == nil {
		return
	}
	if err := json.Unmarshal([]byte(text), v); err != nil {
		t.Fatalf("%s: output %q is not the expected JSON: %v", name, text, err)
	}
}

// callToolErr calls a tool expecting a refusal, and hands back what the agent
// would read.
func callToolErr(t testing.TB, session *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, text := callTool(t, session, name, args)
	if !res.IsError {
		t.Fatalf("%s succeeded, want a refusal: %s", name, text)
	}
	return text
}

func TestMCPServerAdvertises(t *testing.T) {
	app, functionsDir, _ := manageApp(t)
	session := mcpSession(t, app, functionsDir, unrestricted)

	t.Run("the seven tools", func(t *testing.T) {
		res, err := session.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("ListTools() failed: %v", err)
		}

		got := make(map[string]*mcp.Tool, len(res.Tools))
		for _, tool := range res.Tools {
			got[tool.Name] = tool
		}
		want := []string{
			"list_functions", "get_function", "create_function", "update_function",
			"delete_function", "invoke_function", "get_function_logs",
		}
		if len(got) != len(want) {
			t.Errorf("tools = %d, want %d: %v", len(got), len(want), got)
		}
		for _, name := range want {
			tool, ok := got[name]
			if !ok {
				t.Errorf("tool %q is missing", name)
				continue
			}
			if tool.Description == "" {
				t.Errorf("tool %q carries no description", name)
			}
			if tool.InputSchema == nil {
				t.Errorf("tool %q carries no input schema", name)
			}
		}

		// The two guardrails an agent has to read *before* it decides to call.
		if d := got["delete_function"].Description; !strings.Contains(d, "history") || !strings.Contains(d, "no undo") {
			t.Errorf("delete_function description does not say what it destroys: %q", d)
		}
		if d := got["update_function"].Description; !strings.Contains(d, "plainEnv") {
			t.Errorf("update_function description does not name the plainEnv trap: %q", d)
		}

		// A trigger field an agent cannot see in the schema is a trigger it will
		// never propose. Read off the marshalled schema rather than the struct:
		// what is under test is what reaches the other end.
		schema, err := json.Marshal(got["create_function"].InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range []string{"kind", "startupDelayMinutes", "1439"} {
			if !strings.Contains(string(schema), marker) {
				t.Errorf("the create_function schema does not carry %q", marker)
			}
		}
	})

	t.Run("the instructions carry the writing contract", func(t *testing.T) {
		init := session.InitializeResult()
		if init == nil {
			t.Fatal("the session carries no initialize result")
		}
		// One marker per point of the contract, so a rewrite that drops one is
		// caught rather than merely reformatted.
		for _, marker := range []string{
			"stdin", "stdout", "stderr", // execution contract
			"hyphens",      // naming rule
			"1048576",      // size caps
			"background",   // installs do not finish with the write
			"day-of-week",  // cron expression
			"startup",      // the second kind of trigger
			"1439",         // and the bound on its delay
			"empty object", // plainEnv erases
			"do not patch", // a write replaces
		} {
			if !strings.Contains(init.Instructions, marker) {
				t.Errorf("the instructions do not mention %q", marker)
			}
		}
	})
}

func TestMCPListFunctions(t *testing.T) {
	app, functionsDir, fn := manageApp(t)

	t.Run("lists what the key may reach", func(t *testing.T) {
		session := mcpSession(t, app, functionsDir, unrestricted)

		var out struct {
			Functions []functionSummary `json:"functions"`
			Count     int               `json:"count"`
		}
		callToolOK(t, session, "list_functions", nil, &out)

		if out.Count != 1 || len(out.Functions) != 1 {
			t.Fatalf("count = %d, functions = %v, want exactly the echo function", out.Count, out.Functions)
		}
		if out.Functions[0].Name != "echo" || out.Functions[0].Id != fn.Id {
			t.Errorf("functions[0] = %+v, want the echo record", out.Functions[0])
		}
	})

	t.Run("a scope naming nothing that exists lists nothing", func(t *testing.T) {
		session := mcpSession(t, app, functionsDir, []string{"nosuchfunction"})

		var out struct {
			Count int `json:"count"`
		}
		callToolOK(t, session, "list_functions", nil, &out)
		if out.Count != 0 {
			t.Errorf("count = %d, want 0", out.Count)
		}
	})
}

func TestMCPGetFunction(t *testing.T) {
	app, functionsDir, fn := manageApp(t)
	session := mcpSession(t, app, functionsDir, unrestricted)

	t.Run("answers the contract, and only the contract", func(t *testing.T) {
		res, text := callTool(t, session, "get_function", map[string]any{"idOrName": "echo"})
		if res.IsError {
			t.Fatalf("get_function reported an error: %s", text)
		}

		var contract functionContract
		if err := json.Unmarshal([]byte(text), &contract); err != nil {
			t.Fatalf("output %q is not a contract: %v", text, err)
		}
		if contract.Id != fn.Id || contract.Name != "echo" || contract.Script == "" {
			t.Errorf("contract = %+v, want the echo record", contract)
		}

		// The mechanics the HTTP contract hides stay hidden here: the tool is a
		// facade over the same operation, not a second contract.
		for _, hidden := range []string{"bunLock", `"env"`, "s3cr3t"} {
			if strings.Contains(text, hidden) {
				t.Errorf("the output leaks %s: %s", hidden, text)
			}
		}
	})

	t.Run("refuses a segment designating nothing", func(t *testing.T) {
		if got := callToolErr(t, session, "get_function", map[string]any{"idOrName": "nope"}); !strings.Contains(got, "not found") {
			t.Errorf("error = %q, want a not-found refusal", got)
		}
	})

	t.Run("refuses a malformed segment", func(t *testing.T) {
		if got := callToolErr(t, session, "get_function", map[string]any{"idOrName": "../etc"}); !strings.Contains(got, "invalid function name") {
			t.Errorf("error = %q, want an invalid-name refusal", got)
		}
	})
}

func TestMCPCreateFunction(t *testing.T) {
	app, functionsDir, _ := manageApp(t)
	session := mcpSession(t, app, functionsDir, unrestricted)

	t.Run("writes the function, its triggers and its secrets", func(t *testing.T) {
		var contract functionContract
		callToolOK(t, session, "create_function", map[string]any{
			"name":        "greet",
			"script":      "console.log('{}')",
			"packageJson": `{"dependencies":{}}`,
			"plainEnv":    map[string]any{"TOKEN": "v"},
			"triggers": []any{
				map[string]any{"name": "nightly", "schedule": "0 3 * * *", "payload": map[string]any{"n": 1}},
			},
		}, &contract)

		if contract.Name != "greet" || contract.Id == "" {
			t.Fatalf("contract = %+v, want a named function carrying an id", contract)
		}
		if len(contract.Triggers) != 1 || contract.Triggers[0].Schedule != "0 3 * * *" {
			t.Errorf("contract.Triggers = %+v, want the nightly trigger", contract.Triggers)
		}
		if got := secretsOf(t, app, contract.Id); got["TOKEN"] != "v" {
			t.Errorf("secrets = %v, want TOKEN stored", got)
		}
	})

	t.Run("refuses a name the rule does not allow", func(t *testing.T) {
		got := callToolErr(t, session, "create_function", map[string]any{
			"name":   "my_function",
			"script": "console.log('{}')",
		})
		if !strings.Contains(got, "my_function") {
			t.Errorf("error = %q, want the refused name quoted back", got)
		}
	})

	t.Run("refuses a restricted scope", func(t *testing.T) {
		scoped := mcpSession(t, app, functionsDir, []string{"whatever"})
		got := callToolErr(t, scoped, "create_function", map[string]any{
			"name":   "sneaky",
			"script": "console.log('{}')",
		})
		if !strings.Contains(got, "restricted scope") {
			t.Errorf("error = %q, want a scope refusal", got)
		}
	})
}

// TestMCPUpdateFunction covers the guardrail this layer exists for: the PUT it
// ends up calling replaces the function whole, so what the caller did not send
// has to be re-sent from what is stored.
func TestMCPUpdateFunction(t *testing.T) {
	t.Run("a script alone leaves packageJson intact", func(t *testing.T) {
		app, functionsDir, fn := manageApp(t)
		session := mcpSession(t, app, functionsDir, unrestricted)
		before := fn.GetString("packageJson")
		if before == "" {
			t.Fatal("the fixture carries no packageJson, so this test proves nothing")
		}

		var contract functionContract
		callToolOK(t, session, "update_function", map[string]any{
			"idOrName": "echo",
			"script":   "console.log('{\"v\":2}')",
		}, &contract)

		if contract.Script != `console.log('{"v":2}')` {
			t.Errorf("script = %q, want the one just sent", contract.Script)
		}
		if contract.PackageJson != before {
			t.Errorf("packageJson = %q, want it preserved at %q", contract.PackageJson, before)
		}
	})

	t.Run("no plainEnv leaves the secrets intact", func(t *testing.T) {
		app, functionsDir, fn := manageApp(t)
		session := mcpSession(t, app, functionsDir, unrestricted)

		callToolOK(t, session, "update_function", map[string]any{
			"idOrName": "echo",
			"script":   "console.log('{}')",
		}, nil)

		if got := secretsOf(t, app, fn.Id); got["TOKEN"] != "s3cr3t" {
			t.Errorf("secrets = %v, want TOKEN preserved", got)
		}
	})

	t.Run("an explicit empty plainEnv erases them", func(t *testing.T) {
		app, functionsDir, fn := manageApp(t)
		session := mcpSession(t, app, functionsDir, unrestricted)

		callToolOK(t, session, "update_function", map[string]any{
			"idOrName": "echo",
			"plainEnv": map[string]any{},
		}, nil)

		if got := secretsOf(t, app, fn.Id); len(got) != 0 {
			t.Errorf("secrets = %v, want none left", got)
		}
	})

	t.Run("no triggers leaves the triggers intact, an empty list removes them", func(t *testing.T) {
		app, functionsDir, fn := manageApp(t)
		session := mcpSession(t, app, functionsDir, unrestricted)
		createTestTrigger(t, app, "nightly", "0 3 * * *", fn.Id, true)

		callToolOK(t, session, "update_function", map[string]any{
			"idOrName": "echo",
			"script":   "console.log('{}')",
		}, nil)
		if got := triggersOf(t, app, fn.Id); len(got) != 1 {
			t.Fatalf("triggers = %v, want the one that was there", got)
		}

		callToolOK(t, session, "update_function", map[string]any{
			"idOrName": "echo",
			"triggers": []any{},
		}, nil)
		if got := triggersOf(t, app, fn.Id); len(got) != 0 {
			t.Errorf("triggers = %v, want none left", got)
		}
	})

	t.Run("an empty packageJson does clear the dependencies", func(t *testing.T) {
		app, functionsDir, _ := manageApp(t)
		session := mcpSession(t, app, functionsDir, unrestricted)

		var contract functionContract
		callToolOK(t, session, "update_function", map[string]any{
			"idOrName":    "echo",
			"packageJson": "",
		}, &contract)

		if contract.PackageJson != "" {
			t.Errorf("packageJson = %q, want it cleared", contract.PackageJson)
		}
	})
}

func TestMCPDeleteFunction(t *testing.T) {
	app, functionsDir, fn := manageApp(t)
	session := mcpSession(t, app, functionsDir, unrestricted)

	var out map[string]string
	callToolOK(t, session, "delete_function", map[string]any{"idOrName": "echo"}, &out)
	if out["deleted"] != "echo" {
		t.Errorf("output = %v, want the deleted segment named", out)
	}
	if _, err := app.FindRecordById(faasboxFunctionsCollection, fn.Id); err == nil {
		t.Error("the record survived the deletion")
	}
}

func TestMCPInvokeFunction(t *testing.T) {
	app, functionsDir, _ := invokeMCPApp(t)
	session := mcpSession(t, app, functionsDir, unrestricted)

	t.Run("answers the result, stderr and the duration", func(t *testing.T) {
		var out mcpInvokeResult
		callToolOK(t, session, "invoke_function", map[string]any{
			"idOrName": "echo",
			"payload":  map[string]any{"hello": "world"},
		}, &out)

		if out.Function != "echo" {
			t.Errorf("function = %q, want echo", out.Function)
		}
		result, ok := out.Result.(map[string]any)
		if !ok || !strings.Contains(result["echo"].(string), "world") {
			t.Errorf("result = %v, want the payload echoed back", out.Result)
		}
		if out.Stderr != "diagnostic\n" {
			t.Errorf("stderr = %q, want the diagnostic line", out.Stderr)
		}
		if out.DurationMs < 0 {
			t.Errorf("duration_ms = %d, want a duration", out.DurationMs)
		}
	})

	t.Run("a run that failed still publishes what it produced", func(t *testing.T) {
		res, text := callTool(t, session, "invoke_function", map[string]any{"idOrName": "boom"})
		if !res.IsError {
			t.Fatalf("invoke_function succeeded on a failing function: %s", text)
		}

		var out mcpInvokeResult
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("output %q is not an invocation result: %v", text, err)
		}
		if out.Error == "" {
			t.Error("the failed invocation carries no error message")
		}
		if !strings.Contains(out.Stderr, "nope") {
			t.Errorf("stderr = %q, want what the function wrote before failing", out.Stderr)
		}
	})

	t.Run("a refusal publishes nothing but its reason", func(t *testing.T) {
		if got := callToolErr(t, session, "invoke_function", map[string]any{"idOrName": "absent"}); !strings.Contains(got, "not found") {
			t.Errorf("error = %q, want a not-found refusal", got)
		}
	})
}

func TestMCPGetFunctionLogs(t *testing.T) {
	app, functionsDir, fn := manageApp(t)
	session := mcpSession(t, app, functionsDir, unrestricted)

	recordExecution(app, logEntry{
		FunctionId: fn.Id, FunctionName: "echo", Trigger: "cron", Status: "success",
		DurationMs: 12, Stdout: `{"ok":true}`, Stderr: "diag", ExitCode: 0,
	})

	var out struct {
		Logs  []logContract `json:"logs"`
		Count int           `json:"count"`
	}
	callToolOK(t, session, "get_function_logs", map[string]any{"idOrName": "echo"}, &out)

	if out.Count != 1 || len(out.Logs) != 1 {
		t.Fatalf("count = %d, logs = %v, want the single entry", out.Count, out.Logs)
	}
	entry := out.Logs[0]
	if entry.Trigger != "cron" || entry.Status != "success" || entry.Stderr != "diag" {
		t.Errorf("entry = %+v, want the recorded run", entry)
	}
}

// TestMCPFailureIsClassified pins the line mcpFailure draws. The two halves are
// tested apart because they fail apart: relaying everything loses the server log
// on a fault of ours, masking everything loses the correction loop the layer
// exists for.
func TestMCPFailureIsClassified(t *testing.T) {
	t.Run("a refused record reaches the agent with its field", func(t *testing.T) {
		app, functionsDir, _ := manageApp(t)
		session := mcpSession(t, app, functionsDir, unrestricted)

		// A trigger with no schedule: the hook refuses it, the field being no
		// longer Required — a startup trigger legitimately carries none.
		got := callToolErr(t, session, "create_function", map[string]any{
			"name":     "no-schedule",
			"script":   "console.log('{}')",
			"triggers": []any{map[string]any{"name": "nightly", "schedule": ""}},
		})
		if !strings.Contains(got, "nightly") || !strings.Contains(got, "schedule") {
			t.Errorf("error = %q, want the refused trigger and its field named", got)
		}
	})

	t.Run("a refused cron expression reaches the agent verbatim", func(t *testing.T) {
		app, functionsDir, _ := manageApp(t)
		// The hook main.go binds, and the one refusal that arrives as an
		// *router.ApiError rather than as field validation.
		app.OnRecordCreate(faasboxTriggersCollection).BindFunc(validateTriggerHook)
		session := mcpSession(t, app, functionsDir, unrestricted)

		got := callToolErr(t, session, "create_function", map[string]any{
			"name":     "bad-schedule",
			"script":   "console.log('{}')",
			"triggers": []any{map[string]any{"name": "nightly", "schedule": "not a cron"}},
		})
		if !strings.Contains(got, "not a cron") {
			t.Errorf("error = %q, want the refused expression quoted back", got)
		}
	})

	t.Run("a fault of ours is masked and logged", func(t *testing.T) {
		app, functionsDir, _ := manageApp(t)
		enableServerLogs(t, app)
		session := mcpSession(t, app, functionsDir, unrestricted)

		// Drop the collection the operation reads from. Nothing a caller did,
		// nothing a caller can fix — the shape of every internal failure.
		logsCollection, err := app.FindCollectionByNameOrId(faasboxLogsCollection)
		if err != nil {
			t.Fatal(err)
		}
		if err := app.Delete(logsCollection); err != nil {
			t.Fatal(err)
		}

		got := callToolErr(t, session, "get_function_logs", map[string]any{"idOrName": "echo"})
		if got != "Failed to read the logs of this function" {
			t.Errorf("error = %q, want the generic wording the route answers with", got)
		}
		if strings.Contains(got, faasboxLogsCollection) {
			t.Errorf("error = %q, want nothing of our internals in it", got)
		}

		// The half that matters during an incident: the cause left a trace.
		var logged bool
		for _, message := range serverLogMessages(t, app) {
			if strings.Contains(message, "faasbox mcp: a tool failed") {
				logged = true
			}
		}
		if !logged {
			t.Error("the masked failure left nothing in the server log")
		}
	})
}

// TestMCPScopeRefusals is the half no HTTP scenario can cover: /mcp carries no
// {name} segment, so requireAPIKey never weighs the scope for it. Whatever
// enforcement exists is the operations', reached through the tools.
func TestMCPScopeRefusals(t *testing.T) {
	app, functionsDir, _ := invokeMCPApp(t)

	// Scoped on a function that exists, aimed at another one that also exists:
	// the refusal must not depend on the target being absent.
	other, err := resolveFunction(app, "someotherfunction")
	if err != nil {
		t.Fatal(err)
	}
	session := mcpSession(t, app, functionsDir, []string{other.Id})

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"get_function", map[string]any{"idOrName": "echo"}},
		{"invoke_function", map[string]any{"idOrName": "echo"}},
		{"delete_function", map[string]any{"idOrName": "echo"}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			got := callToolErr(t, session, tc.tool, tc.args)
			if !strings.Contains(got, "not authorized for function") {
				t.Errorf("error = %q, want the same scope refusal the route answers", got)
			}
		})
	}

	// And the function is still there: a refused delete deleted nothing.
	if _, err := resolveFunction(app, "echo"); err != nil {
		t.Errorf("the refused delete removed the function anyway: %v", err)
	}
}

// TestMCPEndpointAuth pins what the middleware chain refuses, over HTTP.
func TestMCPEndpointAuth(t *testing.T) {
	app, functionsDir, _ := manageApp(t)

	// A well-formed initialize request: the refusals below happen before the
	// transport ever reads it, and that is exactly what is being pinned.
	const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
		`"protocolVersion":"2025-06-18","capabilities":{},` +
		`"clientInfo":{"name":"test","version":"0"}}}`

	scenarios := []tests.ApiScenario{
		{
			Name:            "no API key is a 401",
			Method:          http.MethodPost,
			URL:             "/mcp",
			Body:            strings.NewReader(initialize),
			Headers:         map[string]string{"Accept": "application/json, text/event-stream"},
			ExpectedStatus:  401,
			ExpectedContent: []string{"Missing X-API-Key header"},
		},
		{
			Name:   "a key without canManage is a 403",
			Method: http.MethodPost,
			URL:    "/mcp",
			Body:   strings.NewReader(initialize),
			Headers: map[string]string{
				"Accept":    "application/json, text/event-stream",
				"X-API-Key": createTestAPIKey(t, app, "invoke-only", nil),
			},
			ExpectedStatus:  403,
			ExpectedContent: []string{"not authorized to manage functions"},
		},
		{
			Name:   "a management key reaches the transport",
			Method: http.MethodPost,
			URL:    "/mcp",
			Body:   strings.NewReader(initialize),
			Headers: map[string]string{
				"Accept":       "application/json, text/event-stream",
				"Content-Type": "application/json",
				"X-API-Key":    createTestManageKey(t, app, "manager", nil, true),
			},
			ExpectedStatus:  200,
			ExpectedContent: []string{`"name":"faasbox"`, `"tools"`},
		},
	}

	for _, scenario := range scenarios {
		s := manageScenario(app, functionsDir, scenario)
		s.Test(t)
	}

	// The signpost is not posted on an instance with no FAASBOX_PUBLIC_URL:
	// announcing a discovery document that is not mounted walks the agent into
	// a wall. manageApp mounts /mcp on that footing.
	noChallenge := manageScenario(app, functionsDir, tests.ApiScenario{
		Name:            "no OAuth, no challenge",
		Method:          http.MethodPost,
		URL:             "/mcp",
		Body:            strings.NewReader(initialize),
		Headers:         map[string]string{"Accept": "application/json, text/event-stream"},
		ExpectedStatus:  401,
		ExpectedContent: []string{"Missing X-API-Key header"},
		AfterTestFunc:   expectNoChallenge,
	})
	noChallenge.Test(t)
}

// TestMCPEndpointOAuth pins the endpoint on the other footing: the signpost that
// starts the flow, a token that opens the session, and the key that still works.
func TestMCPEndpointOAuth(t *testing.T) {
	app, functionsDir, _ := bearerApp(t)
	seedActiveGrant(t, app)

	const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{` +
		`"protocolVersion":"2025-06-18","capabilities":{},` +
		`"clientInfo":{"name":"test","version":"0"}}}`

	scenarios := []tests.ApiScenario{
		{
			// The whole point of the header: an agent handed nothing but a URL
			// learns from it where to go and authenticate.
			Name:            "an unauthenticated request is told where to go",
			Method:          http.MethodPost,
			URL:             "/mcp",
			Body:            strings.NewReader(initialize),
			Headers:         map[string]string{"Accept": "application/json, text/event-stream"},
			ExpectedStatus:  401,
			ExpectedContent: []string{"Missing X-API-Key header"},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				want := `Bearer resource_metadata="` + testOAuthConfig.Issuer +
					`/.well-known/oauth-protected-resource"`
				if got := res.Header.Get("WWW-Authenticate"); got != want {
					t.Fatalf("WWW-Authenticate = %q, want %q", got, want)
				}
			},
		},
		{
			Name:   "a refused token is told the same thing",
			Method: http.MethodPost,
			URL:    "/mcp",
			Body:   strings.NewReader(initialize),
			Headers: map[string]string{
				"Accept":        "application/json, text/event-stream",
				"Authorization": "Bearer " + oauthSecretPrefix + strings.Repeat("z", oauthSecretRandom),
			},
			ExpectedStatus:  401,
			ExpectedContent: []string{"Invalid access token"},
			AfterTestFunc: func(t testing.TB, app *tests.TestApp, res *http.Response) {
				if res.Header.Get("WWW-Authenticate") == "" {
					t.Fatal("a refused token got no challenge")
				}
			},
		},
		{
			// requireManageKey and exposeKeyScope have no branch of their own:
			// the record a token deposits is the record a key deposits.
			Name:   "an access token reaches the transport",
			Method: http.MethodPost,
			URL:    "/mcp",
			Body:   strings.NewReader(initialize),
			Headers: map[string]string{
				"Accept":        "application/json, text/event-stream",
				"Content-Type":  "application/json",
				"Authorization": "Bearer " + testAccessToken,
			},
			ExpectedStatus:  200,
			ExpectedContent: []string{`"name":"faasbox"`, `"tools"`},
		},
		{
			// The key path is untouched by the token path arriving beside it.
			Name:   "a management key still reaches the transport",
			Method: http.MethodPost,
			URL:    "/mcp",
			Body:   strings.NewReader(initialize),
			Headers: map[string]string{
				"Accept":       "application/json, text/event-stream",
				"Content-Type": "application/json",
				"X-API-Key":    createTestManageKey(t, app, "manager-beside-oauth", nil, true),
			},
			ExpectedStatus:  200,
			ExpectedContent: []string{`"name":"faasbox"`, `"tools"`},
		},
		{
			// The OAuth footing is handed to the /mcp group alone, so a key
			// presented anywhere else is weighed exactly as before, scope
			// included. The scope on /mcp itself is pinned by TestExposeKeyScope
			// and TestBearerScopeReachesTheHandler.
			Name:            "a scoped key keeps its scope where OAuth is mounted",
			Method:          http.MethodGet,
			URL:             "/functions",
			Headers:         map[string]string{"X-API-Key": createTestManageKey(t, app, "scoped-beside-oauth", []string{"nothing"}, true)},
			ExpectedStatus:  200,
			ExpectedContent: []string{`"count":0`},
		},
	}

	for _, scenario := range scenarios {
		s := bearerScenario(app, functionsDir, scenario)
		s.Test(t)
	}
}

// The signpost belongs to /mcp alone. Posting it on the routes below would send
// a caller with a curl on a whole OAuth journey to obtain a token they do not
// accept — so these 401s stay bare even where OAuth is mounted.
func TestChallengeIsMCPOnly(t *testing.T) {
	app, functionsDir, _ := bearerApp(t)

	scenarios := []tests.ApiScenario{
		{Name: "invoke", Method: http.MethodPost, URL: "/invoke/echo"},
		{Name: "list functions", Method: http.MethodGet, URL: "/functions"},
		{Name: "read a function", Method: http.MethodGet, URL: "/api/faasbox/functions/echo"},
		{Name: "create a function", Method: http.MethodPost, URL: "/api/faasbox/functions"},
	}

	for _, scenario := range scenarios {
		scenario.ExpectedStatus = 401
		scenario.ExpectedContent = []string{"Missing X-API-Key header"}
		scenario.AfterTestFunc = expectNoChallenge
		s := bearerScenario(app, functionsDir, scenario)
		s.Test(t)
	}
}

// expectNoChallenge fails a scenario whose 401 carried the OAuth signpost.
func expectNoChallenge(t testing.TB, app *tests.TestApp, res *http.Response) {
	if got := res.Header.Get("WWW-Authenticate"); got != "" {
		t.Fatalf("WWW-Authenticate = %q, want no challenge on this response", got)
	}
}

// TestExposeKeyScope pins the bridge between the two containers: requireAPIKey
// leaves the key on the RequestEvent, and a wrapped std handler only ever sees
// the *http.Request. The probe below is that handler.
func TestExposeKeyScope(t *testing.T) {
	app, functionsDir, fn := manageApp(t)

	probe := func(t testing.TB, app *tests.TestApp, e *core.ServeEvent) {
		registerFaaSRoutes(app, e, functionsDir)

		group := e.Router.Group("/mcp-scope-probe")
		group.Bind(requireAPIKey(e.App, apiKeysOnly))
		group.Bind(requireManageKey())
		group.Bind(exposeKeyScope())
		group.GET("", apis.WrapStdHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			allowed, err := keyScopeFromContext(r.Context())
			if err != nil {
				http.Error(w, "unreadable", http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"allowed": allowed})
		})))
	}

	unreadableKey := createTestManageKey(t, app, "broken-scope", []string{fn.Id}, true)
	setKeyScope(t, app, unreadableKey, `{"echo":true}`)

	scenarios := []tests.ApiScenario{
		{
			Name:            "an open scope arrives empty",
			Method:          http.MethodGet,
			URL:             "/mcp-scope-probe",
			Headers:         map[string]string{"X-API-Key": createTestManageKey(t, app, "open", nil, true)},
			ExpectedStatus:  200,
			ExpectedContent: []string{`"allowed":null`},
		},
		{
			Name:            "a restricted scope arrives intact",
			Method:          http.MethodGet,
			URL:             "/mcp-scope-probe",
			Headers:         map[string]string{"X-API-Key": createTestManageKey(t, app, "scoped", []string{fn.Id}, true)},
			ExpectedStatus:  200,
			ExpectedContent: []string{`"allowed":["` + fn.Id + `"]`},
		},
		{
			// Refused by the middleware, before the handler: /mcp carries no
			// {name} segment, so requireAPIKey never looks at the scope, and an
			// unreadable one would otherwise reach the tools as "no restriction".
			Name:            "an unreadable scope never reaches the handler",
			Method:          http.MethodGet,
			URL:             "/mcp-scope-probe",
			Headers:         map[string]string{"X-API-Key": unreadableKey},
			ExpectedStatus:  403,
			ExpectedContent: []string{"API key scope cannot be read"},
		},
	}

	for _, scenario := range scenarios {
		scenario.TestAppFactory = func(t testing.TB) *tests.TestApp { return app }
		scenario.DisableTestAppCleanup = true
		scenario.BeforeTestFunc = probe
		scenario.Test(t)
	}
}

// invokeMCPApp prepares an app with two functions carrying no package.json —
// invoking must not wait on a dependency install here — one echoing its payload,
// one failing on purpose.
func invokeMCPApp(t testing.TB) (*tests.TestApp, string, *core.Record) {
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
	bindTriggerHook(app)

	functionsDir, functions := setupTestFunctions(t, app, map[string]string{
		"echo": `console.error("diagnostic");
const payload = await Bun.stdin.text();
console.log(JSON.stringify({echo: payload}));`,
		"boom": `console.error("nope"); process.exit(3);`,
		// A third one, never invoked: it is what a scoped key is allowed instead.
		"someotherfunction": "",
	})
	return app, functionsDir, functions["echo"]
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pocketbase/pocketbase/core"
)

// The seven tools an MCP session is offered, one per verb the API already
// publishes. mcp.go carries the transport and the instructions; this file
// carries what an agent may do and how each argument is described to it.
//
// **Every tool calls the operation its HTTP route calls**, never the database.
// The scope is a parameter of those operations (cf. backend.md » Gestion des
// fonctions par l'API » Une couche appelable sans requete), which is what makes
// this layer a transport in the same sense manage.go is: a tool that
// reimplemented what its handler does would be a copy diverging at the first
// fix.
//
// Refusals travel through mcpFailure, which draws the same line the HTTP
// transport draws: what the caller is meant to read goes back word for word,
// what is a fault of ours goes to the server log and comes back generic. Both
// sides read that line from classifyManageFailure (manage.go), which is the
// only place it is written.
//
// What this layer *does* add is the three guardrails below, and they are its
// reason to exist. A server that only relayed the API would be a proxy.
//
//   - **update_function reads, merges, then replaces.** The PUT it ends up
//     calling replaces the function whole: a body carrying only "script" empties
//     "packageJson", and the dependencies with it. An agent fixing three lines
//     of code must not destroy the dependencies of the function it is fixing.
//   - **"plainEnv" is never sent unless the caller sent it.** An empty object
//     erases every secret of the function, with no confirmation and no way back.
//     Same rule as applyPlainEnv server-side: what the caller does not say, we do
//     not write. The tool description says so too — the code avoiding the trap is
//     not enough when it is the model that decides what to send.
//   - **delete_function says what it destroys.** The triggers and the whole
//     execution history go with the record. A description that omits it leaves an
//     agent calling it to "tidy up".
//
// Past the 300-line guideline, deliberately, and on the criterion manageops.go
// already applies: what is here is one responsibility — the inventory of what an
// agent may do — and the reader wants that inventory in one place. Cutting it by
// domain would scatter seven ten-line handlers across three files and separate
// the guardrails from the tools that carry them.

// mcpTrigger is one trigger as an agent describes it. It mirrors manageTrigger
// field for field, with two differences that are about schema inference rather than
// meaning: the payload is a plain value rather than json.RawMessage, which would
// infer as a base64 string, and every field carries the sentence the agent reads
// next to it.
type mcpTrigger struct {
	Name     string `json:"name" jsonschema:"a label for this trigger"`
	Schedule string `json:"schedule" jsonschema:"cron expression, five fields: minute hour day-of-month month day-of-week; leave it empty on a startup trigger"`
	Payload  any    `json:"payload,omitempty" jsonschema:"the JSON handed to the function on stdin at each firing"`
	Active   *bool  `json:"active,omitempty" jsonschema:"whether the trigger fires; defaults to true when omitted"`
	MaxQueue int    `json:"maxQueue,omitempty" jsonschema:"how many runs of this trigger may exist at once, waiting plus running; 0 means no limit"`
	Kind     string `json:"kind,omitempty" jsonschema:"cron for a scheduled trigger, startup to fire once when the server comes up; defaults to cron when omitted"`
	// The delay is a whole number of minutes rather than a duration string: it is
	// what the column holds, and what the bound is expressed in.
	StartupDelayMinutes int `json:"startupDelayMinutes,omitempty" jsonschema:"on a startup trigger, how long after the server comes up it fires, in minutes, from 0 to 1439"`
}

// mcpFunctionArgs is what the three tools that only designate a function take.
type mcpFunctionArgs struct {
	IdOrName string `json:"idOrName" jsonschema:"the id or the name of the function; the id is the stable handle and survives a rename"`
}

// mcpCreateArgs is the body create_function builds.
type mcpCreateArgs struct {
	Name        string            `json:"name" jsonschema:"letters, digits and hyphens, starting and ending with a letter or digit, 64 characters at most"`
	Script      string            `json:"script" jsonschema:"the whole index.ts: read the payload with Bun.stdin.text(), write the JSON result with console.log"`
	PackageJson string            `json:"packageJson,omitempty" jsonschema:"the whole package.json, as text; leave it out for a function with no npm dependency"`
	PlainEnv    map[string]string `json:"plainEnv,omitempty" jsonschema:"secrets injected in the environment of the subprocess; leave it out for a function that needs none"`
	Triggers    []mcpTrigger      `json:"triggers,omitempty" jsonschema:"the triggers of the function, scheduled or on startup; leave it out for a function invoked over HTTP only"`
}

// mcpUpdateArgs is what update_function merges onto the stored function.
//
// The two source fields are pointers and the two collections are nil-able, and
// that is the whole guardrail: absent has to be distinguishable from empty, or
// an agent sending only a script would clear everything it did not mention.
type mcpUpdateArgs struct {
	IdOrName    string            `json:"idOrName" jsonschema:"the id or the name of the function to update"`
	Script      *string           `json:"script,omitempty" jsonschema:"the whole new index.ts; leave it out to keep the stored one"`
	PackageJson *string           `json:"packageJson,omitempty" jsonschema:"the whole new package.json; leave it out to keep the stored one, send an empty string to drop the dependencies"`
	PlainEnv    map[string]string `json:"plainEnv,omitempty" jsonschema:"replaces every secret of the function; leave it out to keep them, send an empty object to delete them all"`
	Triggers    []mcpTrigger      `json:"triggers,omitempty" jsonschema:"replaces every trigger of the function; leave it out to keep them, send an empty list to delete them all"`
}

// mcpInvokeArgs is what invoke_function runs.
type mcpInvokeArgs struct {
	IdOrName string `json:"idOrName" jsonschema:"the id or the name of the function to run"`
	Payload  any    `json:"payload,omitempty" jsonschema:"the JSON handed to the function on stdin; defaults to an empty object"`
}

// mcpLogsArgs is what get_function_logs reads.
type mcpLogsArgs struct {
	IdOrName string `json:"idOrName" jsonschema:"the id or the name of the function whose history to read"`
	Limit    int    `json:"limit,omitempty" jsonschema:"how many entries to return, most recent first; 50 by default, 200 at most"`
}

// mcpInvokeResult is what invoke_function publishes, successful or not.
//
// The keys are the ones POST /invoke/{idOrName} answers with, so what an agent
// reads here and what docs/09-api-reference.md documents are the same shape.
// stderr and duration are always carried: an agent debugging a function it just
// wrote needs both, and a run that failed is precisely when they matter.
type mcpInvokeResult struct {
	Function   string `json:"function"`
	Result     any    `json:"result"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr"`
	DurationMs int64  `json:"duration_ms"`
	Truncated  bool   `json:"truncated,omitempty"`
	Error      string `json:"error,omitempty"`
}

// addMCPTools registers the seven tools on a server built for one request, with
// the scope that request carries.
func addMCPTools(s *mcp.Server, app core.App, functionsDir string, allowed []string) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	destructive := true

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_functions",
		Title:       "List functions",
		Description: "List the functions this API key may reach, with their id, their name and their invocation path. A key with a restricted scope sees only what its scope names.",
		Annotations: readOnly,
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		functions, err := listFunctions(app, functionsDir, allowed)
		if err != nil {
			return nil, nil, mcpFailure(app, err, "cannot read the functions")
		}
		return nil, map[string]any{"functions": functions, "count": len(functions)}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_function",
		Title:       "Read a function",
		Description: "Read one function: its script, its package.json, its triggers and where its dependency install stands. Secrets are never returned — they can be written, not read back.",
		Annotations: readOnly,
	}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpFunctionArgs) (*mcp.CallToolResult, any, error) {
		contract, err := getFunction(app, allowed, in.IdOrName)
		if err != nil {
			return nil, nil, mcpFailure(app, err, "Failed to resolve the function")
		}
		return nil, contract, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "create_function",
		Title: "Create a function",
		Description: "Create a function, with its triggers and its secrets. " +
			"The dependencies declared in packageJson install in the background: the depsStatus returned is not the end of the install. " +
			"Fails with a conflict if another function already carries the name, and a key whose scope names precise functions cannot create at all.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpCreateArgs) (*mcp.CallToolResult, any, error) {
		// A payload that will not serialise is the caller's own data, not a
		// failure of the operation: it never reaches classifyManageFailure and
		// travels as it stands.
		triggers, err := mcpTriggers(in.Triggers)
		if err != nil {
			return nil, nil, err
		}
		contract, err := createFunction(app, allowed, manageRequest{
			Name:        in.Name,
			Script:      in.Script,
			PackageJson: in.PackageJson,
			PlainEnv:    mcpPlainEnv(in.PlainEnv),
			Triggers:    triggers,
		})
		if err != nil {
			return nil, nil, mcpFailure(app, err, "Failed to save the function")
		}
		return nil, contract, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "update_function",
		Title: "Update a function",
		Description: "Update a function in place. The tool reads it first and merges what you send onto what is stored, so anything you leave out is preserved: sending only a script keeps the package.json, the triggers and the secrets. " +
			"Sending plainEnv as an empty object deletes every secret of the function, and sending triggers as an empty list deletes every trigger — both are what an explicit empty value means here. " +
			"This never renames: the function keeps the name it has.",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true},
	}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpUpdateArgs) (*mcp.CallToolResult, any, error) {
		// Read before writing: this is the guardrail. The replacement the API
		// performs is whole, so what is not re-sent is what the function loses.
		current, err := getFunction(app, allowed, in.IdOrName)
		if err != nil {
			return nil, nil, mcpFailure(app, err, "Failed to resolve the function")
		}

		req := manageRequest{
			Script:      current.Script,
			PackageJson: current.PackageJson,
			// Absent stays absent, which is what preserves the secrets.
			PlainEnv: mcpPlainEnv(in.PlainEnv),
		}
		if in.Script != nil {
			req.Script = *in.Script
		}
		if in.PackageJson != nil {
			req.PackageJson = *in.PackageJson
		}
		// A nil list leaves the triggers alone; an empty one removes them all.
		if in.Triggers != nil {
			triggers, err := mcpTriggers(in.Triggers)
			if err != nil {
				return nil, nil, err
			}
			req.Triggers = triggers
		}

		contract, err := replaceFunction(app, allowed, in.IdOrName, req)
		if err != nil {
			return nil, nil, mcpFailure(app, err, "Failed to save the function")
		}
		return nil, contract, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "delete_function",
		Title: "Delete a function",
		Description: "Delete a function. Its triggers and its entire execution history are destroyed with it, and there is no undo. " +
			"This is not a way to tidy up: ask before calling it.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive},
	}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpFunctionArgs) (*mcp.CallToolResult, any, error) {
		if err := deleteFunction(app, allowed, in.IdOrName); err != nil {
			return nil, nil, mcpFailure(app, err, "Failed to delete the function")
		}
		return nil, map[string]string{"deleted": in.IdOrName}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "invoke_function",
		Title: "Invoke a function",
		Description: "Run a function now and return what it produced: the parsed result, its stderr and how long it took. " +
			"The function's own code decides what this does, so treat it as an external effect. " +
			"An invocation waits for a pending dependency install rather than failing on it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpInvokeArgs) (*mcp.CallToolResult, any, error) {
		payload, err := mcpRawJSON(in.Payload)
		if err != nil {
			return nil, nil, fmt.Errorf("payload is not serialisable to JSON: %w", err)
		}
		if len(payload) == 0 {
			payload = json.RawMessage("{}")
		}

		// Not routed through mcpFailure, and deliberately: invoking speaks a
		// vocabulary classifyManageFailure does not know — errTooBusy,
		// errExecTimedOut, *errOutputUnusable, and whatever the engine reports —
		// all of it written for whoever called. The one fault of ours on this
		// path is already logged and blanked at the source (errResolveFailed,
		// invokeops.go).
		outcome, err := invokeFunction(ctx, app, functionsDir, allowed, in.IdOrName, payload)
		result := mcpInvokeResult{
			Function:   outcome.Function,
			Result:     outcome.Result,
			Stderr:     outcome.Stderr,
			DurationMs: outcome.Duration.Milliseconds(),
			Truncated:  outcome.Truncated,
		}
		if err == nil {
			return nil, result, nil
		}

		// Same split answerInvokeFailure makes: a refusal has nothing to publish
		// — no function was reached, so there are no streams and no duration —
		// while a run that failed has to hand back everything it produced. An
		// agent that only got "exit status 1" cannot fix the function.
		if outcome.Function == "" {
			return nil, nil, err
		}
		result.Stdout = outcome.Stdout
		result.Error = err.Error()
		return &mcp.CallToolResult{IsError: true}, result, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "get_function_logs",
		Title: "Read a function's history",
		Description: "Read the last runs of a function, most recent first: status, duration, stdout, stderr, exit code and the payload each run received. " +
			"This is the only way to see what a trigger did — a run nobody asked for answers no one, and its log entry is the trace it leaves.",
		Annotations: readOnly,
	}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpLogsArgs) (*mcp.CallToolResult, any, error) {
		logs, err := functionLogs(app, allowed, in.IdOrName, in.Limit)
		if err != nil {
			return nil, nil, mcpFailure(app, err, "Failed to read the logs of this function")
		}
		return nil, map[string]any{"logs": logs, "count": len(logs)}, nil
	})
}

// mcpFailure is what a refused tool call hands back to the agent.
//
// A refusal the caller is meant to read travels **word for word**: that is the
// correction loop this whole layer exists for. classifyManageFailure is used
// here as a predicate and not for its status — an agent has no use for a number,
// and err.Error() says more than the HTTP body does, naming the function that
// was not found or the trigger that was refused.
//
// A fault of ours travels nowhere. Its cause goes to the server log and the
// agent gets the same generic wording the route answers with. Two reasons, and
// the first is the heavier: an internal failure the caller is told about still
// has to leave something behind for whoever reads the logs during an incident —
// the rule the invocation path already applies (invokeops.go). The second is
// that a database error is not something an agent can act on.
func mcpFailure(app core.App, err error, fallback string) error {
	if classifyManageFailure(err) != nil {
		return err
	}

	app.Logger().Error("faasbox mcp: a tool failed", "answer", fallback, "error", err)
	return errors.New(fallback)
}

// mcpPlainEnv turns the secrets an argument carries into the field
// manageRequest expects, and hands back nothing when the caller sent nothing.
//
// nil and an empty map are two different instructions here, and telling them
// apart is the point: nil preserves the stored secrets, `{}` erases them. A
// map[string]string marshals without ever failing, which is why nothing is
// returned but the value.
func mcpPlainEnv(env map[string]string) json.RawMessage {
	if env == nil {
		return nil
	}
	raw, err := json.Marshal(env)
	if err != nil {
		// Unreachable for a map of strings; falling back on nil rather than on
		// an empty object keeps the failure on the preserving side.
		return nil
	}
	return raw
}

// mcpTriggers converts the triggers an agent described into the ones the
// operation takes. A nil list stays nil — the triggers are left alone — while an empty one
// travels as empty, which removes them all.
func mcpTriggers(triggers []mcpTrigger) ([]manageTrigger, error) {
	if triggers == nil {
		return nil, nil
	}

	converted := make([]manageTrigger, 0, len(triggers))
	for i, c := range triggers {
		payload, err := mcpRawJSON(c.Payload)
		if err != nil {
			return nil, fmt.Errorf("the payload of trigger #%d is not serialisable to JSON: %w", i+1, err)
		}
		converted = append(converted, manageTrigger{
			Name:                c.Name,
			Schedule:            c.Schedule,
			Payload:             payload,
			Active:              c.Active,
			MaxQueue:            c.MaxQueue,
			Kind:                c.Kind,
			StartupDelayMinutes: c.StartupDelayMinutes,
		})
	}
	return converted, nil
}

// mcpRawJSON re-encodes a decoded JSON value. An absent value stays absent
// rather than becoming the literal `null`, which the callers read as "nothing
// was sent".
func mcpRawJSON(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

package main

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
)

// This file mounts the MCP server: the transport, the per-request construction,
// and the instructions a session receives before it calls anything. The seven
// tools live in mcptools.go — their schemas and descriptions are a file's worth
// on their own.
//
// It exists because the management API answers "how do I write a function" with
// a list of endpoints and nothing else. An agent handed a URL and a key has to
// be told the contract by its user at every session, and discovers the bounds by
// hitting them. MCP is the protocol that closes exactly that gap: the server
// declares its tools with their schemas, and the client reads them on connect.
//
// What is served here is a facade over manageops.go and invokeops.go, in the
// same sense manage.go is: a transport. No tool touches the database. The only
// thing this layer adds on top of the HTTP contract is the guardrails that
// contract cannot carry because it has to stay predictable (cf. mcptools.go).

// faasboxVersion is what the MCP implementation advertises. A released image
// carries its tag, injected at build time through
// -ldflags="-X main.faasboxVersion=..." from the FAASBOX_VERSION build argument
// the production Dockerfile takes. Anything built any other way keeps "dev",
// which is the honest answer for a binary that belongs to no release.
var faasboxVersion = "dev"

// mcpKeyContextKey names the standard-context slot where exposeKeyScope leaves
// the authenticated key record.
//
// It is a distinct type rather than a string so nothing else can collide with
// it, which is the rule for context keys — and it is *not* apiKeyContextKey:
// that one names a slot on the RequestEvent, a different container entirely.
type mcpKeyContextKey struct{}

// exposeKeyScope carries the authenticated key from the RequestEvent onto the
// standard request context.
//
// It is the bridge this route needs and no other does. requireAPIKey leaves the
// key record on the *core.RequestEvent (e.Set), which requestKeyScope reads back
// — but apis.WrapStdHandler hands the MCP handler nothing but e.Request, and
// getServer(r *http.Request) never sees what e.Set deposited. Without this
// middleware the handler would run with no scope at all, which is to say with
// every function of the instance in reach.
//
// It also *validates* the scope, and answers 403 on one it cannot read. That
// refusal has to happen here: requireAPIKey only weighs a scope on a route whose
// path carries a {name} segment (apikeys.go, step 6), and /mcp carries none, so
// nothing upstream would catch an unreadable one. An unreadable restriction is
// not an absence of restriction — reading it as such widens access instead of
// narrowing it.
//
// Bound after requireAPIKey, which is what leaves the record behind. A superuser
// session short-circuits that middleware and leaves none: nothing is deposited,
// and keyScopeFromContext reads that as unrestricted, exactly as
// requestKeyScope does.
func exposeKeyScope() *hook.Handler[*core.RequestEvent] {
	return &hook.Handler[*core.RequestEvent]{
		Id: "faasboxExposeKeyScope",
		Func: func(e *core.RequestEvent) error {
			record, ok := e.Get(apiKeyContextKey).(*core.Record)
			if !ok || record == nil {
				return e.Next()
			}

			if _, err := readKeyScope(record); err != nil {
				e.App.Logger().Error("faasbox: unreadable allowedFunctions, denying the MCP session",
					"keyName", record.GetString("name"), "error", err)
				return e.JSON(http.StatusForbidden, map[string]string{
					"error": errScopeUnreadable.Error(),
				})
			}

			e.Request = e.Request.WithContext(
				context.WithValue(e.Request.Context(), mcpKeyContextKey{}, record))
			return e.Next()
		},
	}
}

// keyScopeFromContext is requestKeyScope on the standard context: same record,
// same reading, same two invariants. A context with no record means a superuser
// session or a key declaring no scope — unrestricted either way — and an
// unreadable scope comes back as an error the caller must deny on.
func keyScopeFromContext(ctx context.Context) ([]string, error) {
	record, ok := ctx.Value(mcpKeyContextKey{}).(*core.Record)
	if !ok || record == nil {
		return nil, nil
	}
	return readKeyScope(record)
}

// mcpHandler returns the http.Handler serving /mcp over Streamable HTTP.
//
// **Stateless, and that is a security decision rather than a simplification.**
// A stateful streamable server builds one Server per *session*: getServer runs
// at initialize, and every later request rides the session id. The scope would
// then be the one the key that opened the session carried, while requireAPIKey
// kept validating whatever key each request presents — two different keys, one
// authorization. Stateless makes getServer run per request, so the scope is
// always the presented key's. It is also the direction the spec is moving
// (SEP-2567).
//
// The schema cache is what makes that affordable: without it every request would
// pay the reflection pass that infers the seven input schemas. The SDK ships it
// for this exact deployment shape.
func mcpHandler(app core.App, functionsDir string) http.Handler {
	cache := &mcp.SchemaCache{}

	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		allowed, err := keyScopeFromContext(r.Context())
		if err != nil {
			// exposeKeyScope already refused this request, so this branch is the
			// fail-closed guard rather than the path taken: a getServer that
			// swallowed the error would fall back on an unrestricted scope.
			app.Logger().Error("faasbox mcp: unreadable allowedFunctions, refusing the session",
				"error", err)
			return nil
		}
		return newMCPServer(app, functionsDir, allowed, cache)
	}, &mcp.StreamableHTTPOptions{Stateless: true})
}

// newMCPServer builds the server one request works with, carrying that request's
// authorization.
func newMCPServer(app core.App, functionsDir string, allowed []string, cache *mcp.SchemaCache) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    "faasbox",
			Title:   "FaaSBox",
			Version: faasboxVersion,
		},
		&mcp.ServerOptions{
			Instructions: mcpInstructions,
			SchemaCache:  cache,
		},
	)
	addMCPTools(server, app, functionsDir, allowed)
	return server
}

// mcpInstructions is what a session receives at initialization, before the first
// tool is called. It carries the contract for writing a FaaSBox function, which
// is written nowhere a machine receives without being handed it.
//
// Distilled from docs/ — 03-writing-functions.md for the execution contract, the
// naming rule and the caps, 05-scheduling-cron.md for the expression, and
// 09-api-reference.md for what a write replaces. It lives in the code rather
// than in a served file so what an agent receives follows the version of the
// server serving it.
//
// Six points, and it stays at six. Everything else belongs to the description of
// the tool it concerns, which arrives with that tool's schema; piling it up here
// would make the whole thing unreadable for the agent as much as for us.
const mcpInstructions = `FaaSBox runs short TypeScript/JavaScript functions on Bun. Each invocation is a
separate subprocess. These tools write, run and inspect those functions.

What you need to know before writing one:

1. Execution contract. The payload arrives on stdin as JSON, the result is JSON
   on stdout, and stderr is captured as diagnostics. A working skeleton:

       const body = JSON.parse((await Bun.stdin.text()) || "{}");
       console.error("diagnostics go to stderr");
       console.log(JSON.stringify({ ok: true, body }));

2. Naming. Letters, digits and hyphens, starting and ending with a letter or a
   digit, 64 characters at most. This is refused at save time, not merely
   recommended: a name outside the rule is a 400 and nothing is written.

3. Size. "script" and "packageJson" are capped at 1048576 characters each, and
   the secrets of one function at roughly 75 KB of plaintext.

4. Dependencies install in the background, after the write returns. The
   "depsStatus" a write hands back is therefore not the end of the install: read
   the function again to follow it, or simply invoke it — an invocation waits for
   a pending install rather than failing. An install that fails leaves the
   function with no dependencies at all until its "packageJson" is fixed.

5. Cron schedules are five fields: minute hour day-of-month month day-of-week.
   A trigger points at the function itself, so renaming it keeps it firing.

6. Writes replace, they do not patch. update_function reads the function first
   and merges what you send onto what is stored, so a field you leave out is
   preserved — but a field you send as an empty string does clear it, and
   "plainEnv" sent as an empty object deletes every secret of the function.
   Leave "plainEnv" out unless you mean to rewrite the whole set.
`

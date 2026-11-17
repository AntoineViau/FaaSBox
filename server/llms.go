package main

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"
)

// This file serves /llms.txt: the guide an agent reads when all it holds is an
// API key and the address of this instance — no MCP client, nothing to
// configure. The user pastes one prompt, the agent reads this, then calls the
// management API with the key.
//
// The name follows the llms.txt proposal (llmstxt.org): a Markdown file at the
// root, opening on an H1 and a quoted summary. What follows departs from it on
// purpose — the proposal fills its sections with links to Markdown pages, and
// this instance has none to link to, while the docs on GitHub would describe
// another version than the one answering. So the sections carry the content.
//
// It teaches the same contract as the MCP instructions, read from the same
// source (agentcontract.go), and adds what MCP declares through its tool
// schemas: the routes, the bodies, the traps. The guardrails a tool applies in
// code — update_function merging what the agent left out — do not exist here;
// the sixth point of the contract says so instead, which is weaker, and the
// documentation says that too.
//
// Public, like the convention it follows: an agent fetching a page sends no
// header. That is why two things are left out. The version, which is not public
// (cf. versionHandler). And any origin read off the request: Host and
// X-Forwarded-* are whatever the caller says, and FAASBOX_PUBLIC_URL is absent
// precisely on the instances where a key is the only way in. Paths are relative
// to wherever the file was fetched from; the prompt the AI Agent page hands out
// carries the absolute address.

// llmsTxtHandler answers GET /llms.txt. The guide is composed once, when the
// route is mounted: everything it depends on is read at startup and does not
// move.
func llmsTxtHandler(instanceName string, pages pagesConfig) func(*core.RequestEvent) error {
	body := llmsTxt(instanceName, pages)
	return func(re *core.RequestEvent) error {
		return re.String(http.StatusOK, body)
	}
}

// llmsTxt composes the guide: the title the MCP server announces, the summary,
// the HTTP API, the writing contract with its sixth point as it holds over
// HTTP, and where this instance serves its public pages.
func llmsTxt(instanceName string, pages pagesConfig) string {
	_, title := mcpIdentity(instanceName)
	return "# " + title + "\n\n" +
		llmsSummary + "\n\n" +
		llmsHTTPAPI + "\n\n" +
		"## Writing a function\n\n" +
		writingContract + "\n\n" + httpWritesReplace + "\n\n" +
		"## Public pages\n\n" +
		pagesParagraph(pages) + "\n"
}

// llmsSummary is the quoted summary the proposal puts right under the title.
// It also points a client that speaks MCP to /mcp, where the same rights come
// with a merge the agent does not have to perform.
const llmsSummary = `> FaaSBox runs short TypeScript functions on Bun, called over HTTP, on a
> schedule, or as public pages. This file is for an agent holding an API key of
> this instance: it is all you need to write, run and inspect functions over
> plain HTTP. A client that speaks MCP should connect to /mcp instead — the same
> rights and the same contract, and its update tool merges for you.`

// llmsHTTPAPI is what MCP declares through its tool schemas and descriptions,
// written for an agent with curl.
//
// Distilled from docs/09-api-reference.md, sections 1 to 4, and from
// docs/13-ai-agents.md for the traps. Code samples are indented rather than
// fenced: this is a Go raw string, and a backtick would close it.
const llmsHTTPAPI = `## Calling this instance

Every path below is relative to the origin you fetched this file from. Send the
key on every request:

    X-API-Key: fbx_...

Writing, reading and deleting functions needs a key carrying the "Can manage
functions" flag (canManage). Creating one also needs an open scope: a key
restricted to some functions changes and deletes those, and creates none.
Bodies are JSON unless said otherwise. Every route under /api/ shares a rate
limit; over it, the answer is 429.

{idOrName} designates a function by its id or its name. The id never changes
and survives a rename: use it once you have it.

## Routes

    GET    /functions                         what the key reaches
    POST   /api/faasbox/functions             create         201, 409 if the name is taken
    GET    /api/faasbox/functions/{idOrName}  read           200, 404
    PUT    /api/faasbox/functions/{idOrName}  replace whole  200, 404: never creates, never renames
    DELETE /api/faasbox/functions/{idOrName}  delete         204
    GET    /api/faasbox/functions/{idOrName}/logs?limit=N    the last runs, newest first
    POST   /invoke/{idOrName}                 run it now

GET /functions answers {"functions":[{"id","name","invoke"}],"count"}. A 401
means the key is missing or wrong, a 403 that it lacks canManage, has expired,
or does not reach that function.

## The body of POST and PUT

    {
      "name": "hello",
      "script": "const req = JSON.parse(await Bun.stdin.text());\nconst { name } = JSON.parse(req.body || \"{}\");\nconsole.log(JSON.stringify({ hello: name }));",
      "packageJson": "{\"dependencies\":{}}",
      "sampleBody": "{\"name\":\"Ada\"}",
      "sampleHeaders": [{ "name": "Content-Type", "value": "application/json" }],
      "plainEnv": { "API_TOKEN": "..." },
      "triggers": [
        { "name": "nightly", "kind": "cron", "schedule": "0 3 * * *", "timezone": "Europe/Paris", "payload": {}, "active": true },
        { "name": "boot", "kind": "startup", "startupDelayMinutes": 5, "active": true },
        { "name": "public", "kind": "route", "active": true }
      ]
    }

- "name" is read by POST only. On PUT the path designates the function, and a
  "name" in the body may only repeat it.
- "script" is the source, "packageJson" a package.json as a string.
- "sampleBody" and "sampleHeaders" are the sample call saved with the function:
  the request the editor replays when someone clicks Run. Write the call the
  function expects. Everyone who can read the function reads its sample, so put
  the shape of a signed request there, never a live secret.
- "plainEnv" becomes the function's environment (process.env). It is written
  and never read back: GET does not return it. Real secrets go there. Like
  "triggers", it is the complete set, not a patch: see point 6 below.
- "triggers" is the complete set, not a patch. "kind" left out reads as "cron".
  A "cron" trigger needs a "schedule"; a "startup" or "route" trigger must
  carry none. A "payload" reaches a cron or startup run as the envelope's
  "body", as a string.
- The function and its triggers are written together, or not at all.

POST, GET and PUT answer the function: "id", "name", "script", "packageJson",
"sampleBody", "sampleHeaders", "triggers", plus "depsStatus" and "depsError" for
the dependency install. A refusal is a 400 naming what is wrong:
{"error": "...", "fields": {...}}. A rule that depends on the kind of a trigger
answers with "error" alone: read the message.

DELETE removes the function with its triggers and its whole execution history,
and there is no undo. Ask the user before deleting anything.

## Running and reading what ran

POST /invoke/{idOrName} takes any UTF-8 body — JSON, text, XML, a form — up to
1 MB by default, and hands it to the function as the "body" of an HTTP
envelope. Your X-API-Key never reaches the function. A run that worked answers:

    {"function":"hello","result":{...},"duration_ms":42,"stderr":"","truncated":false}

"result" is what the function printed on stdout, parsed when it is JSON. A run
that failed answers 500 with "error", "stdout" and "stderr": read them to fix
the function. A 500 whose error is about dependencies means the install failed:
fix "packageJson". 504 means the run went past 30 seconds, 429 that every
execution slot is taken.

GET /api/faasbox/functions/{idOrName}/logs answers the last runs, newest first:
50 by default, "limit" up to 200. It is the only way to see what a cron or
startup run did. Each entry carries "trigger", "status", "stdout", "stderr",
"requestPayload", "exitCode" and "created".

## What this key cannot do

Create API keys, read a secret back, or reach anything but the functions it
designates and their history.`

// httpWritesReplace is the sixth point of the contract over HTTP.
//
// Nothing merges for the agent here: PUT replaces the function whole, and the
// one protection left is the agent reading this before its first PUT. It names
// the safe gesture — GET, change, PUT the whole object back — which keeps the
// secrets because a GET response carries none.
//
// The trap it has to spell out in full is plainEnv: not only the empty object,
// but a non-empty one, which replaces every secret. An agent "adding a variable"
// sends one, and since it cannot read the others back, it cannot merge them —
// MCP says the same in the schema of update_function.
const httpWritesReplace = `6. Writes replace, they do not patch. PUT replaces the whole function: a field
   the body leaves out becomes empty, so a body carrying only "script" clears
   "packageJson" — and the dependencies with it — and the sample call. GET the
   function, change what you mean to change, and PUT the whole object back: a
   GET response is accepted as is, and carries no "plainEnv", so sending it back
   keeps the secrets. "plainEnv" and "triggers" are the exception, and the trap:
   left out they are preserved, but sent they replace the whole set. A
   "plainEnv" listing one variable deletes every other secret of the function,
   and you cannot read them back to merge: send it only to rewrite all of them,
   with values the user gave you. "plainEnv" sent as an empty object deletes
   every secret, and "triggers" sent as [] deletes every trigger.`

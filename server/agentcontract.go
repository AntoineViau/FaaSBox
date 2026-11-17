package main

// This file holds what an agent is taught about writing a FaaSBox function,
// whatever way it reaches the instance. Two readers compose it: the MCP server,
// which hands it to a session at initialization (mcpInstructionsFor, mcp.go),
// and the guide served at /llms.txt, which an agent holding nothing but a key
// reads before its first call (llmsTxt, llms.go). Written once, so the two never
// teach different contracts.
//
// Distilled from docs/ — 03-writing-functions.md for the execution contract, the
// naming rule, the caps and the folder each function writes into, 05-triggers.md
// for the triggers, and 09-api-reference.md for what a write replaces. It lives
// in the code rather than in a file on disk so what an agent receives follows
// the version of the server serving it.
//
// Six points, and it stays at six. The first five are the contract and are
// shared. The sixth, what a write replaces, depends on the transport — a tool
// that merges for the agent and a PUT that does not are two different warnings —
// so each reader appends its own. Everything else belongs to whoever owns it:
// the description of the tool it concerns on MCP, the API section of the guide
// over HTTP. Piling it up here would make the whole thing unreadable for the
// agent as much as for us.

// writingContract is the opening paragraph and the first five points, and
// nothing that names a transport: no tool, no route, no verb.
//
// The opening paragraph carries the disk, because an agent that had only "a
// separate subprocess" read it as "stateless", and refused its user a database
// FaaSBox can hold. Its snippet concatenates rather than using a template
// literal: this is a Go raw string, and a single backtick would close it.
const writingContract = `FaaSBox runs short TypeScript/JavaScript functions on Bun. Each invocation is a
separate subprocess, but not a stateless one: every function has a folder of its
own on the server's disk, and what it writes there is still there on its next
invocation. That is enough for a local database — bun:sqlite ships with Bun,
with nothing to install:

    import { Database } from "bun:sqlite";
    const db = new Database(import.meta.dir + "/data.sqlite", { create: true });

Build paths from import.meta.dir: the working directory is the root of all
functions, not the function's own folder. Concurrent invocations are concurrent
processes, so let SQLite do the locking. That folder is deleted with the
function, and FaaSBox neither backs it up nor restores it: a host whose
filesystem does not survive a redeployment starts it empty.

What you need to know before writing one:

1. Execution contract. stdin carries an **envelope** describing the call, the
   result is JSON on stdout, and stderr is captured as diagnostics. A working
   skeleton:

       const req = JSON.parse(await Bun.stdin.text());
       const data = JSON.parse(req.body || "{}");
       console.error("diagnostics go to stderr");
       console.log(JSON.stringify({ ok: true, data }));

   "body" is always a **string**, carrying what was sent byte for byte — never a
   parsed object. That is what makes a webhook signature verifiable and what
   lets a function be handed text, XML or a form-encoded payload; parse it
   yourself when you expect JSON, as the second line above does.

   The rest of the envelope follows the trigger. An HTTP call carries
   {"trigger":"http","method","path","query","headers","body"}, with header
   names lowercased and a repeated header or query parameter joined by ", ".
   A trigger carries {"trigger":"cron"|"startup","triggerName","body"}, and
   triggerName is how a function wired on two schedules tells them apart. An
   invocation made over MCP carries {"trigger":"mcp","body"} — there is no
   request behind it, so nothing else is there to read. A request on a public
   route carries the HTTP shape with "trigger":"route".

   Four headers never reach a function, whatever their casing: x-api-key,
   authorization, cookie and proxy-authorization. They authenticate the caller,
   and a function is never told who called with what credential.

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

5. A trigger points at the function itself, so renaming it keeps it firing. It
   comes in three kinds. A "cron" trigger carries a schedule of five fields:
   minute hour day-of-month month day-of-week. A "startup" trigger carries no
   schedule and fires once when the server comes up, "startupDelayMinutes"
   after it — 0 to 1439. A startup trigger is armed at boot and nowhere else:
   creating or changing one does not fire it, it waits for the next start.
   A cron expression is read in the timezone of its own trigger: "timezone"
   carries an IANA zone name such as "Europe/Paris", and omitting it means UTC.
   A "route" trigger carries no schedule and **publishes** the function: anyone,
   without a key, can then call it at /f/{idOrName} on the instance's pages host.
   On that route stdout must be a response envelope — {"status","headers","body"},
   "body" a string and required — or the caller gets a 502. Add one only when
   the user asked for a public endpoint.`

// pagesParagraph is where this instance serves its public pages, which both
// readers close on.
//
// It is not a seventh point. Like the instance's name in the MCP identity, it is
// a fact about this server rather than a rule of the contract: an agent that
// publishes a function has to be able to tell the user where the page is, and
// nothing else it can reach says so — the API hands back /invoke paths, and the
// instance route is not something an agent is told to read. Without pages it
// says that too, so the agent does not hand out an address that answers 404 —
// without naming a cause: an empty origin covers an absent variable, a refused
// one and a showcase alike, and only the startup line knows which.
func pagesParagraph(pages pagesConfig) string {
	if pages.Origin == "" {
		return `This instance serves no public page: the operator has public pages off, and
the startup log of the server says why. A "route" trigger is stored but
publishes nothing, and /f/ answers 404, until they are turned on. Say so rather
than handing out an address.`
	}
	return `Public pages on this instance are served at ` + pages.Origin + `: a function
published by a "route" trigger answers at ` + pages.Origin + `/f/{idOrName},
and nowhere else. Give the user that address once you have published one.`
}

# 09 - API Reference

FaaSBox provides a set of HTTP endpoints for managing and invoking functions. All endpoints (except `/health` and `/api/faasbox/instance`) require authentication.

## Authentication
Use the `X-API-Key` header with a valid API key.
```text
X-API-Key: fbx_your_key_here
```

## Rate limits
Every endpoint under `/api/` — the management routes below, the key creation route, and PocketBase's own — is behind a rate limiter: 300 requests per 10 seconds, and 2 per 3 seconds on authentication. Over the limit, the answer is `429 Too Many Requests`. `POST /invoke/{idOrName}` is under no such rule; the `429` it can return means something else entirely, and is documented with it. See [Rate Limiting](06-api-keys-and-security.md#5-rate-limiting).

---

## 1. Invoke a Function
**Endpoint**: `POST /invoke/{idOrName}`

Executes the function the path segment designates.

- **Path Parameter**: `{idOrName}` - The **id** or the **name** of the function. Both work; see [Which one the segment designates](#which-one-the-segment-designates) below.
- **Request Body**: Any valid JSON (max 1 MB by default, `FAASBOX_MAX_BODY_SIZE`).
- **Response**:
    ```json
    {
      "function": "echo",
      "result": { ... },
      "duration_ms": 42,
      "stderr": "...",
      "truncated": false
    }
    ```
    `function` is always the **name**, whichever spelling reached it.
- **Error Codes**:
    - `400`: The path segment is not a usable identifier.
    - `401`: Missing or invalid API key.
    - `403`: Key disabled, expired, not authorized for this function, or carrying a scope that cannot be read (see [06 - API Keys & Security](06-api-keys-and-security.md)). A key with a restricted scope also gets `403` — never `404` — for a segment designating nothing, so it cannot use the two codes to map the instance.
    - `404`: Function not found.
    - `413`: Request body too large (default 1 MB, configurable via `FAASBOX_MAX_BODY_SIZE`).
    - `429`: Too many concurrent invocations (default 4, configurable via `FAASBOX_MAX_CONCURRENCY`).
    - `500`: The function's dependencies could not be installed, so it never ran. The message carries the end of the `bun install` output (see [03 - Writing Functions](03-writing-functions.md)), and the same text lands in `depsError` on the function record.
    - `502`: The function ran, but its output was cut at the capture limit and what remains is not valid JSON. The result is incomplete and is not returned. The error message carries the effective limit and names `FAASBOX_MAX_OUTPUT_SIZE`, the variable that raises it.
    - `504`: Function timed out (> 30s).

    A `500` from a failed install looks like this:
    ```json
    {
      "error": "failed to install dependencies: error: package nope@1.0.0 not found",
      "stdout": "",
      "stderr": "",
      "duration_ms": 843
    }
    ```

    `duration_ms` is the whole time the call spent on dependencies before giving up — including any wait for an install already running on the same function — and the invocation is recorded in the logs like any other failure.

    A `502` body looks like this:
    ```json
    {
      "function": "export-report",
      "error": "function output exceeded the 1048576 bytes capture limit and the truncated result is not valid JSON; raise FAASBOX_MAX_OUTPUT_SIZE or return less data",
      "truncated": true,
      "stderr": "...",
      "duration_ms": 128
    }
    ```

    Truncation alone is not an error: an output cut below the limit that still parses as JSON, or that was free text to begin with, is returned normally with `"truncated": true`. The refusal only fires when the truncation is what broke the parsing.

### Which one the segment designates

A function has two handles: a **name**, which you choose and can change, and an **id**, which FaaSBox assigns once and never changes. The URL accepts either.

The rule is a single one, and it applies everywhere a function is named in a URL:

> The segment is looked up **as an id first**. If no function carries that id — and in every other case — it is looked up **as a name**. **The id wins**: if one function carries the id `X` and another is named `X`, `/invoke/X` reaches the first.

That last sentence is not hypothetical. A name may contain the same characters as an id, so nothing about the look of a segment settles which one it is. The consequence is worth knowing: naming a function after another function's id makes it unreachable by name. Ids are 15 lowercase letters and digits, so this only happens on purpose.

**Which one should you use?** The id, for anything that has to keep working. A name is a label meant to be changed, and changing it breaks every caller wired on it — FaaSBox will not redirect the old URL to the new one. The id is stable for the life of the function, and it is what `GET /functions` hands you.

The same rule governs the management endpoints below, and `GET /api/faasbox/functions/{idOrName}/logs`, `.../env`, `.../files` and `.../files/content`.

---

## 2. List Functions
**Endpoint**: `GET /functions`

Returns the functions **the presented key is allowed to invoke** — not necessarily every function on the server.

- **Response**:
    ```json
    {
      "functions": [
        { "id": "k9m2xq7p4wz1n3v", "name": "echo", "invoke": "/invoke/echo" },
        { "id": "b3t8rl5c2fh6d9s", "name": "ping-site", "invoke": "/invoke/ping-site" }
      ],
      "count": 2
    }
    ```
- **Error Codes**:
    - `401`: Missing or invalid API key.
    - `403`: Key disabled, expired, or carrying a scope that cannot be read.

`id` is the stable handle: it is what a key's scope lists, and it survives a rename. `invoke` is spelled with the name because that is the readable form — `/invoke/{id}` reaches the same function and is what to wire an integration on.

The same `allowedFunctions` scope that governs `/invoke/{idOrName}` governs this listing (see [06 - API Keys & Security](06-api-keys-and-security.md)). A key restricted to one function sees that one and nothing else; `count` reflects the filtered list, not the real total. A superuser session sees everything.

What decides whether a function is listed is the presence of its `index.ts` **on disk**, not the state of its record. A function whose script is empty has none, whatever its history: saving an empty script removes the file, so the function stops being listed and `/invoke` answers `404` until you save a script over it again. Its directory stays where it is, `package.json` and installed dependencies included.

A key whose scope names only functions that no longer exist gets `{"functions": [], "count": 0}` — an empty list, not an error.

---

## 3. Manage Functions

Four endpoints write functions over HTTP, with an API key rather than a superuser token: create, read, replace, delete. There is no `PATCH`.

| Endpoint | Effect | Codes |
|---|---|---|
| `POST /api/faasbox/functions` | Creates a function | `201`, `400`, `401`, `403`, `409` |
| `GET /api/faasbox/functions/{idOrName}` | Reads it | `200`, `400`, `401`, `403`, `404` |
| `PUT /api/faasbox/functions/{idOrName}` | Replaces it | `200`, `400`, `401`, `403`, `404` |
| `DELETE /api/faasbox/functions/{idOrName}` | Deletes it | `204`, `400`, `401`, `403`, `404` |

**Authentication**: an API key carrying the `canManage` flag (see [06 - API Keys & Security](06-api-keys-and-security.md)), or a superuser session. A valid key *without* the flag gets `403` on all four — invoking and rewriting are two different rights.

**Scope**: the key's `allowedFunctions` applies to the three endpoints that name a function, exactly as it does to `/invoke`. `POST` is the exception: it carries no function in its path, and there is nothing to compare a scope against for a function that does not exist yet. **A key with a restricted scope therefore changes and deletes the functions it names, but creates none** — otherwise it could grant itself a function it was never given.

### Request body (`POST` and `PUT`)

```json
{
  "name": "echo",
  "script": "const payload = await Bun.stdin.text(); console.log(JSON.stringify({ok: true}));",
  "packageJson": "{\"dependencies\":{}}",
  "plainEnv": { "STRIPE_KEY": "sk_test_..." },
  "crons": [
    { "name": "nightly", "schedule": "0 3 * * *", "payload": {}, "active": true, "maxQueue": 1 }
  ]
}
```

- `name` is read by `POST` only. On `PUT` the **path** identifies the function; a `name` in the body may repeat that identity — either the current name or the id, so a `GET` response can be edited and sent straight back — but a different one is a `400`. **This route never renames.** A silent rename would break every URL wired on the old name with nothing in the exchange saying so; rename from the editor instead.
- `script` and `packageJson` are **replaced whole**. `PUT` replaces the function: a field the body does not carry becomes empty. Sending only `script` therefore clears `packageJson`, and with it the dependencies. Send the full pair every time.
- `crons`, when present, is the complete set of triggers, not a patch. `active` defaults to `true` when omitted, `maxQueue` to `0` (no limit).
- **The whole request is one write, or none of it is.** The function and its triggers are saved together: if any trigger is refused, the call answers `400` and *nothing* is applied — not the script, not `packageJson`, not `plainEnv`, not the other triggers. A refused `POST` leaves no function behind, so the corrected retry still creates rather than colliding with a `409`.

### `plainEnv`: absent and empty do not mean the same thing

This is the trap of the contract, and it is worth reading twice:

| `plainEnv` in the body | Effect |
|---|---|
| **Absent**, or `null` | The stored secrets are **preserved**. |
| `{}` | Every secret is **deleted**. |
| A non-empty object | The secrets are **replaced whole** — variables not listed are dropped. |

A client that serialises an empty object where it meant to send nothing destroys the secrets of that function, with no confirmation and no way back. If you are writing a script or an agent tool around this endpoint, say so in its description.

Anything other than an object of string values is a `400`, and nothing is written.

Reading secrets back is *not* part of this contract: `GET` never returns them. Only the superuser endpoint below decrypts them.

### `crons`: same rule

| `crons` in the body | Effect |
|---|---|
| **Absent**, or `null` | The existing triggers are **preserved**. |
| `[]` | Every trigger is **deleted**. |
| A non-empty list | The triggers are **replaced whole**. |

### Response (`POST`, `GET`, `PUT`)

```json
{
  "id": "k9m2xq7p4wz1n3v",
  "name": "echo",
  "script": "...",
  "packageJson": "...",
  "depsStatus": "installing",
  "depsError": "",
  "crons": [
    { "name": "nightly", "schedule": "0 3 * * *", "payload": {}, "active": true, "maxQueue": 1 }
  ]
}
```

`DELETE` answers `204` with no body.

**Never `env`, never `bunLock`.** The encrypted environment and the lockfile are mechanics, not contract: a caller that never saw them cannot be broken by them changing. Use `id` for anything that has to keep working — it survives a rename, and it is what a key's scope lists.

`depsStatus` is what the record says at that instant, not the end of the install: `bun install` runs in the background and the reply does not wait for it. Poll with `GET`, or simply call `POST /invoke/{idOrName}` — the invocation path installs what is missing before running.

### What each code means

- `400`: the body is not valid JSON, the name is not a usable identifier, `plainEnv` is not an object of strings, a `name` in a `PUT` body designates another function, or a cron expression is invalid. It is also the answer when a record is refused by field validation, and the body then names the field:
    ```json
    { "error": "The function was refused", "fields": { "script": "Must be no more than 1048576 character(s)." } }
    ```
    A trigger refused the same way says which one, since a function and a trigger both carry a `name`:
    ```json
    { "error": "Trigger \"nightly\" was refused", "fields": { "schedule": "Cannot be blank." } }
    ```
    `script` and `packageJson` are capped at **1,048,576 characters each** — the editor uses the same ceiling. It is counted in characters, not bytes, so a non-ASCII file gets more than a megabyte of room.

    `plainEnv` has its own ceiling, about **75 KB of secrets in clear**. It applies to the encrypted form actually stored, and encryption plus its encoding cost a third on top, which is where the figure comes from.
- `401`: missing or invalid `X-API-Key`.
- `403`: the key lacks `canManage`, its scope does not cover this function, its scope cannot be read, or it is restricted and tried to create.
- `404`: the segment designates no function. `PUT` **never creates** — a `404` is a `404`.
- `409`: `POST` on a name another function already carries.

### Deleting

`DELETE` removes the record. Its triggers and its execution logs go with it, by the cascade on their relation, and its folder is removed from disk. **The history is destroyed** — that is deliberate, in the same spirit as log retention, and there is no undo.

### Example

```bash
curl -X POST http://localhost:8080/api/faasbox/functions \
  -H "X-API-Key: fbx_..." \
  -H 'Content-Type: application/json' \
  -d '{"name":"echo","script":"console.log(JSON.stringify({ok:true}))","packageJson":"{}"}'
```

---

## 4. Read a Function's Logs
**Endpoint**: `GET /api/faasbox/functions/{idOrName}/logs`

Returns the execution history of one function, most recent first.

**Authentication**: an API key carrying the `canManage` flag, or a superuser session — the same right as [Manage Functions](#3-manage-functions) above, and its `allowedFunctions` scope applies here exactly as it does there. A valid key *without* the flag gets `403`. The bar is higher than invocation on purpose: an entry carries the `requestPayload` and the output of runs **someone else triggered**, which an invocation response never shows you.

This is the endpoint to reach for when the trigger is a cron. A scheduled run answers no HTTP response, so the log entry is the only trace it leaves: without this, knowing whether last night's run succeeded took a superuser token.

- **Query Parameter**: `limit`, the number of entries to return.

    | `limit` | Result |
    |---|---|
    | Absent or empty | 50 |
    | A whole number from 1 to 200 | that many |
    | Above 200 | capped at 200 |
    | Not a number, `0`, or negative | **400** |

    The two refusals differ deliberately. `limit=1000` is a legitimate ask the server bounds; `limit=abc` is an ask nobody can honour, and answering the default would return something other than what you requested. There is **no pagination**: with a default retention of 1000 entries and a ceiling of 200, you cannot walk the whole history of a chatty function. The endpoint is for debugging the last runs.

- **Response**:
    ```json
    {
      "logs": [
        {
          "id": "9d1x4kq7mz2p8rb",
          "functionName": "noisy",
          "trigger": "http",
          "status": "success",
          "durationMs": 42,
          "stdout": "{\"ok\":true}",
          "stderr": "diag",
          "requestPayload": { "n": 3 },
          "exitCode": 0,
          "truncated": false,
          "created": "2026-08-09T10:12:31Z"
        }
      ],
      "count": 1
    }
    ```
- `count` reflects the list returned, not the total the function carries.
- A function that never ran returns `{"logs": [], "count": 0}` with a `200`. Nothing there is not an error.
- **No `function` field**: the endpoint is already per function, and the identity is in the path. `functionName` stays because it says something else — the name the function carried **when it ran**, deliberately not refreshed (see [07 - Execution Logs](07-execution-logs.md)). A rename therefore does not split the history: the entries written under the old name keep coming back, still spelling it.
- `created` is RFC3339 UTC, the same format as `modified` in the file endpoints below — not PocketBase's own layout.
- `requestPayload` is relayed as stored, and is `null` for an entry that carries none. **Expect two shapes**: a payload cut at the 4 KB storage cap is no longer valid JSON, so it comes back as an escaped **string** instead of an object. Code reading this endpoint has to handle both.
- **Error Codes**:
    - `400`: the segment is not a usable identifier, or `limit` cannot be read.
    - `401`: missing or invalid `X-API-Key`.
    - `403`: the key lacks `canManage`, or its scope does not cover this function.
    - `404`: the segment designates no function.

### Example

```bash
curl -s "http://localhost:8080/api/faasbox/functions/noisy/logs?limit=5" \
  -H "X-API-Key: fbx_..."
```

---

## 5. MCP Endpoint
**Endpoint**: `POST /mcp`

Serves this instance's functions to an AI agent over the [Model Context Protocol](https://modelcontextprotocol.io), in **Streamable HTTP**. It is not a REST endpoint: the body is JSON-RPC, and a client speaks it for you. See [13 - AI Agents](13-ai-agents.md) for how to plug one in.

**Authentication**: two forms, and this is the one endpoint that takes either.

- An API key carrying the `canManage` flag, in the same `X-API-Key` header as everywhere else, or a superuser session. The right required is the one [Manage Functions](#3-manage-functions) requires, and for the same reason: a tool that writes a function decides what this server executes.
- An **OAuth access token**, in `Authorization: Bearer fbo_...`, obtained through the flow in [OAuth Authorization](#6-oauth-authorization). A token is worth an API key carrying `canManage` with no scope restriction — nothing downstream tells the two apart.

Presenting both is not an error: the **key wins** and the token is ignored. One credential is weighed per request.

A request with no credential answers `401` carrying the signpost that starts the flow, provided `FAASBOX_PUBLIC_URL` is set:

```text
WWW-Authenticate: Bearer resource_metadata="https://your-instance/.well-known/oauth-protected-resource"
```

**This header is served on `/mcp` and nowhere else.** `/invoke`, `/functions` and the management routes take an API key and only an API key, so advertising a discovery document to them would send a caller with a `curl` on a whole OAuth journey to obtain a token they do not accept. It is also absent when `FAASBOX_PUBLIC_URL` is unset, since the document it names is then not served.

The endpoint is **stateless**: every request carries its own authorization, and no session outlives the request that opened it. `GET /mcp` therefore answers `405` — there is no server-to-client stream to open.

**Scope**: the key's `allowedFunctions` applies to every tool exactly as it applies to the routes they mirror. A key with a restricted scope reads, changes, runs and deletes the functions it names, and — as on `POST /api/faasbox/functions` — **creates none**. A token declares no scope, so it reaches every function.

- **Tools**:

    | Tool | Mirrors |
    |---|---|
    | `list_functions` | `GET /functions` |
    | `get_function` | `GET /api/faasbox/functions/{idOrName}` |
    | `create_function` | `POST /api/faasbox/functions` |
    | `update_function` | `GET` then `PUT`, merged — see below |
    | `delete_function` | `DELETE /api/faasbox/functions/{idOrName}` |
    | `invoke_function` | `POST /invoke/{idOrName}` |
    | `get_function_logs` | `GET /api/faasbox/functions/{idOrName}/logs` |

    Each one calls the same code its route calls, so the refusals above are the refusals a tool reports — they arrive as a tool error the agent can read and act on, rather than as an HTTP status. A refused name, a refused field, an invalid cron expression and a scope refusal therefore reach the agent **word for word**. A failure on the server's side does not: it is written to the server log and the agent is told only that the call failed, exactly as the matching route answers `500` with a fixed wording.

    `update_function` is the one that is not a plain relay. `PUT` replaces the function whole, so the tool **reads it first and merges** what the caller sent onto what is stored: a call carrying only `script` keeps the `packageJson`, the triggers and the secrets. The explicit empty values still mean what they mean everywhere else — `plainEnv` as `{}` deletes every secret, `crons` as `[]` deletes every trigger, and `packageJson` as `""` clears the dependencies.

- **Instructions**: the session receives the contract for writing a FaaSBox function at initialization — the `stdin`/`stdout` contract, the naming rule, the size caps, the background install, the cron format, and what a write replaces. Nothing has to be pasted into the agent.
- **Error Codes**:
    - `400`: the body is not a valid MCP message, or `Accept` does not carry both `application/json` and `text/event-stream`.
    - `401`: no credential, an invalid `X-API-Key`, or a token that does not pass — unknown, expired, revoked, or issued for another resource. The refusal never says which, and carries the `WWW-Authenticate` above.
    - `403`: the key lacks `canManage`, is disabled or expired, or carries a scope that cannot be read.
    - `405`: on `GET` and `DELETE`, which a stateless server does not serve.

---

## 6. OAuth Authorization

FaaSBox can also act as an **OAuth 2.1 authorization server**, so an agent connects by opening a browser and clicking *Authorize* instead of being handed a key to paste. The endpoints below are what a client drives; the tokens they issue are accepted on [`/mcp`](#5-mcp-endpoint) and nowhere else.

**They only exist if `FAASBOX_PUBLIC_URL` is set.** An authorization server has to publish its own address, and FaaSBox refuses to guess it: without that variable every route in this section answers `404`, and a line at startup says so. See [10 - Deployment](10-deployment.md#the-environment-variables).

The value must be a bare origin — `https://faasbox.example.com`, no path, no query, no fragment — and `http://` is accepted on a loopback host only. Anything else is refused with a message naming the reason, and the routes stay down.

### Discovery

Three public documents, no authentication, readable cross-origin.

| Endpoint | Standard | What it says |
|---|---|---|
| `GET /.well-known/oauth-protected-resource` | RFC 9728 | `/mcp` is the resource, and this instance is the authorization server guarding it |
| `GET /.well-known/oauth-protected-resource/mcp` | RFC 9728 §3 | the same document, at the form that carries the resource path |
| `GET /.well-known/oauth-authorization-server` | RFC 8414 | where `/authorize`, `/token` and `/register` are, and what the server supports |

The last one declares `S256` as the only PKCE method, `none` as the only token endpoint authentication method, `faasbox` as the only scope, and `authorization_response_iss_parameter_supported: true`.

### `POST /oauth/register`

Dynamic client registration (RFC 7591), **unauthenticated**, as the standard requires. Registering is not access: a registered client holds nothing until a human authorizes it.

- **Request Body**: `{"client_name": "my agent", "redirect_uris": ["http://127.0.0.1:41234/callback"]}`. At least one absolute redirect URI is required.
- **Response**: `201` with the metadata echoed back plus a `client_id`. **No `client_secret` is ever issued** — an agent on your machine cannot keep one, so the client is public and PKCE secures the exchange.
- **400** with `invalid_redirect_uri` if `redirect_uris` is missing, empty, relative, or carries a fragment.

Registrations that no authorization ever follows are deleted after twenty-four hours.

### `GET /oauth/authorize`

Takes `response_type=code`, `client_id`, `redirect_uri`, `code_challenge`, `code_challenge_method=S256`, `resource`, and optionally `state`. PKCE and `resource` (RFC 8707) are both **required**.

- An unknown `client_id`, or a `redirect_uri` that does not match one the client registered, is refused **on the spot** with `400`. Nothing is redirected: sending a browser to an unvalidated URI is an open redirect.
- A loopback redirect URI — `http://127.0.0.1:<port>/...` or `http://localhost:<port>/...` — matches whatever port it listens on (RFC 8252). Every other URI has to match exactly.
- Any other problem redirects to the registered URI with an OAuth `error`, the `state` you sent, and an `iss` naming this server (RFC 9207).
- A valid request records the demand and redirects the browser to `/consent`, in the editor. You sign in if you are not already, read what the token would grant, and click. Approving mints an authorization code, valid **one minute and one use**.

### `POST /oauth/token`

Form-encoded, no client authentication.

| `grant_type` | Parameters |
|---|---|
| `authorization_code` | `code`, `code_verifier`, `client_id`, `redirect_uri`, `resource` |
| `refresh_token` | `refresh_token`, `client_id`, `resource` |

- **Response**: `access_token`, `token_type: Bearer`, `expires_in` (one hour), `refresh_token` (ninety days), `scope: faasbox`.
- **The refresh token rotates.** Every use returns a new one and retires the old, and presenting a retired one **revokes the whole authorization** — it means two holders. The same is true of an authorization code presented twice.
- **400** with the OAuth error code: `invalid_grant` for an unknown, replayed, expired or mismatched credential, `invalid_target` for a missing or foreign `resource`, `unsupported_grant_type` otherwise.

### One scope, and it grants everything

There is nothing to choose on the consent screen. A token issued here is worth an API key carrying `canManage` with no scope restriction: read, write, run and delete any function of the instance, and read its logs. The consent screen says so in words before you click, which is why it is worth reading rather than clicking through.

Tokens are opaque strings, stored only as a SHA-256 hash — a stolen database yields nothing usable.

### Presenting a token

`Authorization: Bearer fbo_...` on `/mcp`. What is checked, in order: the shape this server mints, the authorization the token belongs to, that the authorization is still active, that the token has not expired, and that the `resource` it was issued for is this server's. Every failure is a `401` that does not say which check caught it.

The last one is a second barrier — `/oauth/authorize` already refuses to open an authorization for a foreign resource — and it is what stops a token minted for another MCP server from being replayed here.

### Ending an authorization

From the **AI MCP** page of the editor, or by setting `status` to `revoked` on the `faasbox_oauth_grants` record. Either way it takes effect on the agent's **next call**: the authorization is read on every request, and there is no session to invalidate.

---

## 7. Create API Key
**Endpoint**: `POST /api/faasbox/keys`

**Requires Superuser Authentication** (PocketBase standard `Authorization: Bearer <token>`).

Creates a new hashed API key.

- **Request Body**:
    ```json
    {
      "name": "My App",
      "allowedFunctions": ["k9m2xq7p4wz1n3v", "b3t8rl5c2fh6d9s"],
      "expiresAt": "2027-01-01 00:00:00.000Z"
    }
    ```
- `allowedFunctions` is a list of function **ids** — the values `GET /functions` returns. Names are not accepted: a scope written in names would stop granting anything the day a function was renamed. Leave it out for a key with no restriction, or pass `["*"]` for an explicit wildcard.
- `expiresAt` is optional and RFC3339. Leave it out and the key never expires.
- **Response**:
    ```json
    {
      "key": "fbx_...",
      "name": "My App",
      "note": "Store this key securely. It will not be shown again."
    }
    ```
- **400** if the body is not valid JSON, if `name` is missing or empty, or if `expiresAt` is present but not a valid RFC3339 date. The response names which one: `invalid JSON body`, `"name" is required`, or `"expiresAt" must be an RFC3339 date`.
- An unreadable `expiresAt` is rejected rather than ignored. Dropping it would hand back a key that never expires when you asked for one that does — widening access instead of narrowing it, silently.
- **500** if the key cannot be generated.

---

## 8. Read a Function's Environment
**Endpoint**: `GET /api/faasbox/functions/{idOrName}/env`

**Requires Superuser Authentication** (PocketBase standard `Authorization: Bearer <token>`).

Returns the decrypted environment variables of a function, as a flat JSON object. This is the only endpoint that turns a stored secret back into plaintext; the editor uses it to show what a function carries before you replace it.

- **Response**:
    ```json
    {
      "STRIPE_KEY": "sk_test_...",
      "DB_PASSWORD": "super-secret-pass"
    }
    ```
- A function with no secrets returns `{}`.
- **404** if the segment designates no function, **400** if it is not a usable identifier.
- **500** if the stored value cannot be decrypted — for instance after `FAASBOX_ENCRYPTION_KEY` changed. An empty object is never returned in that case, since it would read as "no variable" and invite an overwrite.

---

## 9. List a Function's Files
**Endpoint**: `GET /api/faasbox/functions/{idOrName}/files`

**Requires Superuser Authentication** (PocketBase standard `Authorization: Bearer <token>`).

Returns **one level** of the function's folder on disk. Never recursive.

- **Query Parameter**: `path`, relative to the function folder. Omit it or leave it empty for the root.
- **Response**:
    ```json
    {
      "path": "",
      "entries": [
        { "name": "node_modules", "dir": true,  "entries": 412, "modified": "2026-08-06T09:12:31Z" },
        { "name": "bun.lock",     "dir": false, "size": 297,    "modified": "2026-08-06T09:12:00Z" },
        { "name": "index.ts",     "dir": false, "size": 412,    "modified": "2026-08-06T09:12:00Z" }
      ]
    }
    ```
- Directories come first, then files, each group sorted by name.
- `size` is carried by files, `entries` by directories. `entries` counts one level of that directory — it is there to tell you what you are about to walk into, and it is never a recursive total.
- A function whose folder does not exist yet — never invoked, no dependencies — returns `{"path": "", "entries": []}` at the root. Nothing there is not an error; a function that does not exist at all is a `404`.
- **400** if the segment is not a usable identifier, if `path` leaves the function folder, or if `path` names a file rather than a directory.
- **404** if the segment designates no function, or if `path` names something that does not exist.

---

## 10. Read a Function's File
**Endpoint**: `GET /api/faasbox/functions/{idOrName}/files/content`

**Requires Superuser Authentication** (PocketBase standard `Authorization: Bearer <token>`).

Two modes on one endpoint, chosen by `download`.

- **Query Parameters**: `path` (required, relative to the function folder), `download=1` to fetch the file instead of reading it.
- **Response** without `download`, for a textual file under the view limit:
    ```json
    {
      "path": "index.ts",
      "size": 412,
      "content": "const payload = await Bun.stdin.text();\n…"
    }
    ```
- **415** if the file is binary, with `{"error": "binary"}`. The verdict is a NUL byte in the first 8 KB, never the extension — `node_modules` is full of files without one.
- **413** if the file is larger than `FAASBOX_MAX_FILE_VIEW` (default 256 KB), with `{"error": "too large", "size": 4194304}`.
- **Response** with `download=1`: the file itself, served as `Content-Disposition: attachment`, **with no size limit**. The limit is on what is rendered inline, not on what can be fetched — displaying 40 MB in a browser is a defect, downloading them is not. `415` and `413` do not apply here.
- **400** if the segment is not a usable identifier, if `path` is missing, if `path` names a directory, or if `path` leaves the function folder.
- **404** if the segment designates no function, or if `path` names something that does not exist.

A `path` that leaves the folder is a `400`, never a `404`: the refusal is a rule, not an absence, and answering "not found" would suggest that a different spelling might work.

That refusal covers symbolic links, and that is the point rather than a detail. A function executes arbitrary code with its own folder within reach, so it can drop a link pointing at `/etc/passwd` or at the database and wait for it to be followed. The server therefore resolves links **before** deciding, not after: a check made on the requested path alone would stop a `../..` typed into the URL and let the link through.

Both endpoints are read-only. There is no counterpart that writes, deletes or uploads.

---

## 11. Health Check
**Endpoint**: `GET /health`

**No Authentication Required.**

Used for container health checks or load balancer heartbeat. Verifies that the SQLite database is accessible.

- **Response**: `200 OK`
    ```json
    {
      "status": "ok"
    }
    ```
- **Error Response**: `503 Service Unavailable` (database not accessible)
    ```json
    {
      "status": "unhealthy",
      "error": "database not accessible"
    }
    ```

---

## 12. Instance Mode
**Endpoint**: `GET /api/faasbox/instance`

**No Authentication Required.**

Says whether this instance is a read-only demo. The editor reads it before its
first render, to close the controls a showcase does not offer.

- **Response on a normal instance**: `200 OK`
    ```json
    {
      "demoMode": false
    }
    ```
- **Response on a demo instance**: `200 OK`
    ```json
    {
      "demoMode": true,
      "email": "demo@example.com",
      "password": "demo"
    }
    ```

The two credential fields exist only in demo mode, and they are exactly what
`FAASBOX_DEMOMODE_EMAIL` and `FAASBOX_DEMOMODE_PASSWORD` hold — empty strings
when those are not set. A normal instance never returns them, even if both
variables are set on it.

> ⚠️ This endpoint is public and unauthenticated, so **the credentials of a demo
> instance are published to anyone who asks**. That is the point — the sign-in
> form shows them — but it means the account they name is open to the world,
> the PocketBase admin included. Only ever point them at an instance you are
> content to publish whole.

### What a demo instance refuses

On an instance started with `FAASBOX_DEMOMODE=true`, `GET`, `HEAD` and `OPTIONS`
behave exactly as documented above. **Every other method answers `403`**,
whatever the endpoint:

```json
{
  "error": "this instance is a read-only demo"
}
```

That covers `POST /invoke/{idOrName}`, the whole of [Manage Functions](#3-manage-functions),
[Create an API key](#2-create-an-api-key), `/mcp`, and PocketBase's own
collections API — the admin at `/_/` reads normally and writes nothing.

Three exceptions let a visitor in and keep the editor alive, and they are named
one by one rather than by prefix: `POST /api/collections/_superusers/auth-with-password`,
`POST /api/collections/_superusers/auth-refresh` and `POST /api/realtime`.

**[OAuth](#6-oauth-authorization) is a mixed case**, since only three of its
eight endpoints carry a `POST`. `/oauth/register`, `/oauth/token` and the
consent decision fall with everything else. `GET /oauth/authorize` is refused
too, by name: it records a pending authorization request before redirecting, so
it writes despite its method — and a demo instance no longer runs the hourly
pass that would collect those records. The three discovery documents and the
consent-request lookup stay open; they only read, and the `/agents` page reads
one of them to know what this instance supports.

The practical effect is that no agent can authorize itself against a demo
instance, and none can call it: every MCP message is a `POST`.

Nothing else changes: no collection gains an access rule, and the session a
visitor opens is an ordinary superuser session.

---

## Internal Collections API
Since FaaSBox is built on PocketBase, you can also use the standard PocketBase Web APIs to manage the collections (`faasbox_api_keys`, `faasbox_cron_jobs`, `faasbox_logs`, `faasbox_functions`, and — on an instance that publishes its address — `faasbox_oauth_clients` and `faasbox_oauth_grants`).

Refer to the [PocketBase API Documentation](https://pocketbase.io/docs/api-records/) for details on listing, creating, and updating records in these collections. **Note**: these collections have `nil` API rules, so **only a superuser reaches them** — and a superuser token is full power over the instance: dropping collections, reading every secret in clear, changing the password.

For writing functions, prefer [Manage Functions](#3-manage-functions) above; for reading their history, prefer [Read a Function's Logs](#4-read-a-functions-logs). Both need an API key rather than that token, and both publish a contract rather than the record: the internal columns (`env`, `bunLock`, and whatever the install machinery adds next) can change without breaking what you built on them.

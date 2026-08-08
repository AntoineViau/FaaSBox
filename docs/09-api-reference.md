# 09 - API Reference

FaaSBox provides a set of HTTP endpoints for managing and invoking functions. All endpoints (except `/health`) require authentication.

## Authentication
Use the `X-API-Key` header with a valid API key.
```text
X-API-Key: fbx_your_key_here
```

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

The same rule governs the management endpoints below, and `GET /api/faasbox/functions/{idOrName}/env`, `.../files` and `.../files/content`.

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

What decides whether a function is listed is the presence of its `index.ts` **on disk**, not the state of its record. A function saved with an empty script has never had one written, so it is not invocable and does not show up. Clearing the script of a function that already had one is a different matter: the file stays where it is, so the function keeps being listed and keeps running its previous code until you save a new script over it.

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

## 4. Create API Key
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

## 5. Read a Function's Environment
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

## 6. List a Function's Files
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

## 7. Read a Function's File
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

## 8. Health Check
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

## Internal Collections API
Since FaaSBox is built on PocketBase, you can also use the standard PocketBase Web APIs to manage the collections (`faasbox_api_keys`, `faasbox_cron_jobs`, `faasbox_logs`, `faasbox_functions`).

Refer to the [PocketBase API Documentation](https://pocketbase.io/docs/api-records/) for details on listing, creating, and updating records in these collections. **Note**: these collections have `nil` API rules, so **only a superuser reaches them** — and a superuser token is full power over the instance: dropping collections, reading every secret in clear, changing the password.

For writing functions, prefer [Manage Functions](#3-manage-functions) above. It needs an API key rather than that token, and it publishes a contract rather than the record: the internal columns (`env`, `bunLock`, and whatever the install machinery adds next) can change without breaking what you built on it.

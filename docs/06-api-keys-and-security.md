# 06 - API Keys & Security

FaaSBox is designed with security as a top priority. This guide explains how to manage access and how the underlying security model works.

## The Trust Boundary

Everything below assumes one thing: **the person running the instance is the person who writes its functions.** Their code is trusted by construction — FaaSBox exists to execute it. The boundary the platform defends is the *external caller*, not the function.

That is why the protections here are about who may invoke a function and what leaves the process, not about what the function itself is allowed to do. A function can read files, open outbound connections, and burn CPU; that is the product, not a hole in it.

Found a hole on the other side of that boundary? **Do not open a public issue.** [SECURITY.md](../SECURITY.md) has the private reporting channel, the expected response time, and the full in-scope / out-of-scope list.

## API Key Management

Access to all FaaS endpoints (except `/health`) is restricted by API keys.

### Creating a Key

Two paths, one result. Both go through the same endpoint — the editor simply calls it for you — and both reveal the raw value exactly once.

**From the editor.** Sign in, click **API keys** at the top of the left sidebar, then fill the creation form. The entry only shows up once you have created at least one function, since a key grants access to nothing before that:

1.  A **Name**, for your own reference. Required.
2.  A **Scope**: either *All functions*, or a selection among the functions that exist on the instance.
3.  An optional **Expiration** date. The key stays valid through the end of that day (UTC).

The key is then revealed once, with a copy button and a warning. Dismiss the panel or leave the page and the value is gone for good.

The picker shows you names and records **ids** — see the next section for why. It always writes a well-formed list, `["*"]` when you pick *All functions*, so it cannot produce the malformed scope described below. Typing the field by hand in the admin UI can.

**From the API.** `POST /api/faasbox/keys`, superuser auth required:

```bash
curl -X POST http://localhost:8080/api/faasbox/keys \
  -H "Authorization: $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"my-app","allowedFunctions":["k9m2xq7p4wz1n3v"],"expiresAt":"2026-12-31T23:59:59Z"}'
```

`allowedFunctions` and `expiresAt` are both optional. An empty or absent `allowedFunctions` means the key can invoke any function; an absent `expiresAt` means it never expires. `expiresAt` is a date string — RFC3339 (`2026-12-31T23:59:59Z`) or a plain `2026-12-31`, which lands at midnight UTC. A value that cannot be parsed is rejected with `400`: it is never silently dropped, since that would turn a key you meant to bound into a perpetual one.

### Function Scope

A scope is a list of function **ids** — the values `GET /functions` returns, not the names you read in the editor.

**Why ids.** A name is a field you can edit. When the scope was written in names, renaming `echo` to `echo-v2` silently stopped the key from granting it — and if you later created a *new* function called `echo`, the old key started granting that one instead. Neither event produced any signal: the scope was a list of strings that happened to match, never a reference. With ids, renaming a function changes nothing about who may invoke it, which is the only sane reading of "this key may call that function".

The picker on the **API keys** page does the translation for you: it lists names, it stores ids, and it keeps showing the current name of every function a key grants. An id that no longer matches any function is shown as-is — that is a dangling grant, and seeing it is the only way it ever gets cleaned up.

The `allowedFunctions` field has three states, not two:

| Field | Meaning |
|-------|---------|
| Absent, `null`, or `[]` | No restriction — the key invokes any function. |
| A list of ids, e.g. `["k9m2xq7p4wz1n3v"]` | Only those functions. `["*"]` means all of them. |
| Anything else | **The key is denied**, on every invocation. |

The third case matters if you ever edit a key by hand in the admin UI. A value that is valid JSON but is not a list of strings — an object, a bare string, a list of numbers — cannot be interpreted as a scope. FaaSBox refuses the request with `403` rather than assuming the key is unrestricted, and logs the failure at `Error` level with the key's name so you can find it. A restriction that cannot be read is never treated as an absence of restriction.

A scope written in **names** is not malformed, just wrong: it is a well-formed list that matches no id, so the key grants nothing and `GET /functions` comes back empty. That is the safe direction to fail in, but it is worth recognising if you scripted key creation before this rule existed.

#### The Scope Also Filters the Listing

`GET /functions` returns only the functions the presented key may invoke. A key scoped to one function sees that one alone, and `count` reflects that filtered list. Without this, any valid key would learn the name, id and invocation URL of every function on the instance — the restriction would apply to calling them but not to discovering them.

A superuser session is unrestricted and sees the full list.

**What this does not do: hide that a function exists.** `POST /invoke/{idOrName}` on a function outside your scope still answers `403` and names it, rather than pretending it is not there with a `404`. That is deliberate. Turning the refusal into a `404` would make a legitimate integration painful to debug — you could no longer tell a typo from a missing grant — and it would buy little, since a caller can probe names one at a time either way. FaaSBox treats the *inventory* as worth withholding and the *existence of one named function* as not a secret. If your threat model needs the stronger property, the scope filter is not enough on its own.

A restricted key does get `403` — not `404` — for a segment that designates nothing at all. Otherwise the two codes together would answer "does this name exist?" for every guess, which is exactly the inventory the filter above withholds.

### Key Storage (Hashing)
When a key is generated:
- A random 40-character string is created (prefixed with `fbx_`).
- The server calculates the **SHA-256 hash** of this key.
- **Only the hash is stored in the database.**
- The raw key is returned to you **once**. 

> 🔒 **Security Note**: This means that even if someone gets access to your database, they cannot use the stored hashes to invoke your functions. They would need the original raw key.

### Revocation

From the editor's **API keys** page, each key carries an **Active** checkbox and a delete button — unchecking one disables the key, deleting one removes it. The same two operations are available by hand in the `faasbox_api_keys` collection (`active = false`, or delete the record). Either way, the change takes effect on the next request: nothing is cached.

The page also lets you narrow or widen the scope of a key that already exists, which is the cheap way to apply least privilege after the fact.

Deleting a **function** does not touch the keys scoped to it: their list keeps an id that now designates nothing, which grants nothing. Tidying it is a matter of hygiene, not of security.

---

## Security Layers

### 1. Request Validation
- **Method**: Functions only respond to `POST` requests.
- **Body Size**: Requests larger than 1 MB are rejected with a `413 Payload Too Large` error. Configurable via `FAASBOX_MAX_BODY_SIZE`.
- **Header**: The `X-API-Key` header is mandatory.

### 2. Runtime Isolation
Every function call spawns a new **Bun subprocess**. This ensures:
- **No Shared Memory**: One function cannot access another function's variables or memory.
- **Crash Protection**: If a function crashes or enters an infinite loop, it doesn't affect the main PocketBase server.
- **Process Group Cleanup**: Each subprocess runs in its own process group. On timeout or cancellation, the entire group is killed, preventing zombie child processes.
- **Resource Limits**: We capture and limit the size of `stdout` and `stderr` (1 MB each by default, `FAASBOX_MAX_OUTPUT_SIZE`), and store only the first 8 KB of each in the execution logs (`FAASBOX_MAX_LOG_OUTPUT`).

### 3. Environment Sanitization
When a subprocess is spawned, it does **not** inherit the environment variables of the host system. We provide a minimal, sanitized environment:
- `FUNCTION_NAME`, `NODE_ENV`, `PATH`, `HOME`.
- Only explicitly configured secrets are added.

### 4. Non-Root Execution
In the official Docker image, the application runs as a dedicated `faas` user, not as `root`. This follows the principle of least privilege.

---

## Best Practices

1.  **Least Privilege**: Create specific API keys for specific applications and restrict them to only the functions they need to call.
2.  **Regular Rotation**: Periodically rotate your API keys.
3.  **Secret Management**: Never hardcode API keys or sensitive data in your `index.ts`. Use the [Encrypted Environment Variables](04-environment-variables.md) system.
4.  **Audit Logs**: Regularly check the `faasbox_logs` collection for any suspicious activity or unexpected errors.

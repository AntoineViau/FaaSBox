# 09 - API Reference

FaaSBox provides a set of HTTP endpoints for managing and invoking functions. All endpoints (except `/health`) require authentication.

## Authentication
Use the `X-API-Key` header with a valid API key.
```text
X-API-Key: fbx_your_key_here
```

---

## 1. Invoke a Function
**Endpoint**: `POST /invoke/{name}`

Executes the function with the given name.

- **Path Parameter**: `{name}` - The name of the function (folder name).
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
- **Error Codes**:
    - `400`: Invalid function name.
    - `401`: Missing or invalid API key.
    - `403`: Key disabled, expired, not authorized for this function, or carrying a scope that cannot be read (see [06 - API Keys & Security](06-api-keys-and-security.md)).
    - `404`: Function not found.
    - `413`: Request body too large (default 1 MB, configurable via `FAASBOX_MAX_BODY_SIZE`).
    - `429`: Too many concurrent invocations (default 4, configurable via `FAASBOX_MAX_CONCURRENCY`).
    - `502`: The function ran, but its output was cut at the capture limit and what remains is not valid JSON. The result is incomplete and is not returned. The error message carries the effective limit and names `FAASBOX_MAX_OUTPUT_SIZE`, the variable that raises it.
    - `504`: Function timed out (> 30s).

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

---

## 2. List Functions
**Endpoint**: `GET /functions`

Returns the functions **the presented key is allowed to invoke** — not necessarily every function on the server.

- **Response**:
    ```json
    {
      "functions": [
        { "name": "echo", "invoke": "/invoke/echo" },
        { "name": "ping-site", "invoke": "/invoke/ping-site" }
      ],
      "count": 2
    }
    ```
- **Error Codes**:
    - `401`: Missing or invalid API key.
    - `403`: Key disabled, expired, or carrying a scope that cannot be read.

The same `allowedFunctions` scope that governs `/invoke/{name}` governs this listing (see [06 - API Keys & Security](06-api-keys-and-security.md)). A key restricted to `["echo"]` sees `echo` and nothing else; `count` reflects the filtered list, not the real total. A superuser session sees everything.

A key whose scope names only functions that do not exist gets `{"functions": [], "count": 0}` — an empty list, not an error.

---

## 3. Create API Key
**Endpoint**: `POST /api/faasbox/keys`

**Requires Superuser Authentication** (PocketBase standard `Authorization: Bearer <token>`).

Creates a new hashed API key.

- **Request Body**:
    ```json
    {
      "name": "My App",
      "allowedFunctions": ["echo", "time-now"]
    }
    ```
- **Response**:
    ```json
    {
      "key": "fbx_...",
      "name": "My App",
      "note": "Store this key securely. It will not be shown again."
    }
    ```

---

## 4. Health Check
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

Refer to the [PocketBase API Documentation](https://pocketbase.io/docs/api-records/) for details on listing, creating, and updating records in these collections. **Note**: By default, these collections have `nil` API rules, meaning only Superusers can access them.

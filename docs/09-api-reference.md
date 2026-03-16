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
- **Request Body**: Any valid JSON (max 1 MB).
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
    - `403`: Key disabled, expired, or not authorized for this function.
    - `404`: Function not found.
    - `413`: Request body too large (> 1 MB).
    - `429`: Too many concurrent invocations (default 4, configurable via `FAASBOX_MAX_CONCURRENCY`).
    - `504`: Function timed out (> 30s).

---

## 2. List Functions
**Endpoint**: `GET /functions`

Returns a list of all available functions on the server.

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

# 06 - API Keys & Security

FaaSBox is designed with security as a top priority. This guide explains how to manage access and how the underlying security model works.

## API Key Management

Access to all FaaS endpoints (except `/health`) is restricted by API keys.

### Creating a Key
1.  Use the `POST /api/faasbox/keys` endpoint (superuser auth required). This is the only way to generate a key and see the raw value.
2.  Provide a **Name** (for your reference).
3.  (Optional) Specify **Allowed Functions**. If empty or `["*"]`, the key can invoke any function.
4.  (Optional) Set an **Expiration Date**.

### Key Storage (Hashing)
When a key is generated:
- A random 40-character string is created (prefixed with `fbx_`).
- The server calculates the **SHA-256 hash** of this key.
- **Only the hash is stored in the database.**
- The raw key is returned to you **once**. 

> 🔒 **Security Note**: This means that even if someone gets access to your database, they cannot use the stored hashes to invoke your functions. They would need the original raw key.

### Revocation
To revoke a key, simply delete the record or set `active = false` in the `faasbox_api_keys` collection. The change takes effect immediately.

---

## Security Layers

### 1. Request Validation
- **Method**: Functions only respond to `POST` requests.
- **Body Size**: Requests larger than 1 MB are rejected with a `413 Payload Too Large` error.
- **Header**: The `X-API-Key` header is mandatory.

### 2. Runtime Isolation
Every function call spawns a new **Bun subprocess**. This ensures:
- **No Shared Memory**: One function cannot access another function's variables or memory.
- **Crash Protection**: If a function crashes or enters an infinite loop, it doesn't affect the main PocketBase server.
- **Process Group Cleanup**: Each subprocess runs in its own process group. On timeout or cancellation, the entire group is killed, preventing zombie child processes.
- **Resource Limits**: We capture and limit the size of `stdout` and `stderr` (10 MB each), and store only the first 8 KB of each in the execution logs.

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

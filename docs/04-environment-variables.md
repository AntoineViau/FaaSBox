# 04 - Environment Variables & Secrets

Functions often need sensitive information like API keys, database credentials, or private tokens. FaaSBox provides a secure way to manage these.

## The Problem
Storing secrets in your `index.ts` or as plain text in a database is a security risk. If someone gains access to your repository or database, your secrets are exposed.

## The Solution: AES-256-GCM Encryption
FaaSBox allows you to store secrets that are **encrypted at rest**. They are only decrypted in memory just before the function is executed.

### 1. Enable Encryption
To use secrets, you must provide an encryption key to the FaaSBox server via the `FAASBOX_ENCRYPTION_KEY` environment variable.

```bash
# Generate a random 32-byte key (64 hex characters)
openssl rand -hex 32
```

Pass this to your Docker container:
```bash
-e FAASBOX_ENCRYPTION_KEY=your_64_char_hex_string
```

> ⚠️ **Warning**: If you lose this key, you will not be able to decrypt your secrets. If you change it, existing secrets will become unreadable.

### 2. Configure Secrets for a Function
1.  Open the **PocketBase Admin UI**.
2.  Go to the **faasbox_functions** collection.
3.  Create or Edit a record where the **name** matches your function's folder name.
4.  In the `plainEnv` field, enter your secrets as a JSON object:
    ```json
    {
      "STRIPE_KEY": "sk_test_...",
      "DB_PASSWORD": "super-secret-pass"
    }
    ```
5.  **Save the record**.
    - The server will encrypt this JSON using your `FAASBOX_ENCRYPTION_KEY`.
    - The encrypted blob is stored in the `env` field.
    - The `plainEnv` field is **automatically cleared**.

### 3. Access Secrets in your Function
Secrets are injected into the function's environment and are available via `process.env`.

```typescript
// functions/my-secure-func/index.ts
const apiKey = process.env.STRIPE_KEY;

if (!apiKey) {
  throw new Error("STRIPE_KEY is not defined!");
}

// ... use the key
```

## Built-in Environment Variables
All functions receive these variables by default:
- `FUNCTION_NAME`: The name of the current function.
- `NODE_ENV`: Always set to `production`.
- `HOME`, `PATH`: Standard system paths.

No other environment variables from the host server are leaked to the function processes.

## Server Configuration Variables
These environment variables configure the FaaSBox server itself (not injected into functions):

| Variable | Description | Default |
|----------|-------------|---------|
| `SUPERUSER_EMAIL` | Admin superuser email | *(required)* |
| `SUPERUSER_PASSWORD` | Admin superuser password | *(required)* |
| `FAASBOX_ENCRYPTION_KEY` | 64-char hex key for secrets encryption | *(disabled)* |
| `FAASBOX_MAX_CONCURRENCY` | Max concurrent function executions | `4` |
| `FAASBOX_MAX_LOG_RETENTION` | Max number of execution logs to keep | `1000` |
| `FAASBOX_MAX_OUTPUT_SIZE` | Bytes captured per output stream, `stdout` and `stderr` alike | `1048576` |
| `FAASBOX_MAX_BODY_SIZE` | Bytes accepted in a request body | `1048576` |
| `FAASBOX_MAX_LOG_OUTPUT` | Bytes of `stdout` and `stderr` kept in each log record | `8192` |
| `FAASBOX_MAX_LOG_PAYLOAD` | Bytes of `requestPayload` kept in each log record | `4096` |

Every numeric setting behaves the same way: an absent variable uses the default silently, and a value that is unparsable, negative or zero falls back to the default with a message in the server log. A bad setting never prevents startup.

### Notes on the size limits

`FAASBOX_MAX_OUTPUT_SIZE` applies **per stream**. A single invocation can therefore hold twice that much in memory, `stdout` plus `stderr`.

Capture limits and log limits are independent. `FAASBOX_MAX_OUTPUT_SIZE` bounds what the engine keeps and returns in the HTTP response; `FAASBOX_MAX_LOG_OUTPUT` bounds the copy written to `faasbox_logs`. Setting the log limit above the capture limit is not an error, it just means no log record is ever trimmed.

> ⚠️ Raising `FAASBOX_MAX_LOG_OUTPUT` only takes effect on a database created afterwards. The `faasbox_logs` collection declares its field size from this value when it is first created, and is never revisited. On an existing database, a raised limit lets the server build a value the stored schema rejects, and the log record is silently dropped. Reset `pb_data` after changing it.

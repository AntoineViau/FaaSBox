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

**From the editor.** Open your function and switch to the **Environment** tab. It lists the variables the function currently carries, one row per variable, with the values masked — click **Reveal** to read them.

- **Add** appends an empty row: type a name on the left, its value on the right.
- The trash button removes a row.
- **Save** writes the whole set to the database, encrypted.

This tab saves on its own. The function's own **Save**, next to the name field, and **Save and run** never touch your secrets, so a script you are still editing has no bearing on them, and the reverse holds too.

> ⚠️ **Saving replaces the whole set.** There is no merge: a row you delete is deleted from the function, and saving with no rows at all removes every variable. This is why the tab shows you what is there — editing it blind would drop what you could not see.

Values are readable because the tab is superuser-only, and a superuser can already print `process.env` from any function. Hiding them protected nothing while hiding what a save was about to overwrite.

Three things the tab refuses rather than doing quietly:

- Saving while it could not read the current variables — they are left untouched instead of being replaced by what is on screen.
- The same name twice, since one of the two would silently win.
- A name containing a space or an `=`, which would not reach your function under the name you typed.

A row whose name is left empty is simply ignored when saving.

**From the PocketBase Admin UI.** The same thing by hand: in the **faasbox_functions** collection, write your JSON object in the `plainEnv` field of the record whose **name** matches your function, then save.

Either way, on save:

- The server encrypts the JSON using your `FAASBOX_ENCRYPTION_KEY`.
- The encrypted blob is stored in the `env` field.
- The `plainEnv` field is **automatically cleared**.

A record saved without touching `plainEnv` keeps its secrets — editing a script never disturbs them.

**How much a function may carry.** Roughly **75 KB of secrets in clear**, all variables taken together. The ceiling is on the encrypted blob actually stored, and encryption plus its encoding cost a third on top, which is where that figure comes from. Past it the save is refused and names the field; nothing is ever trimmed behind your back, so a secret that saved is a secret stored whole.

### 3. Access Secrets in your Function
Secrets are injected into the function's environment and are available via `process.env`.

```typescript
// index.ts
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
| `FAASBOX_PUBLIC_URL` | The address this instance answers on, as a bare origin | *(OAuth disabled)* |
| `FAASBOX_MAX_CONCURRENCY` | Max concurrent function executions | `4` |
| `FAASBOX_MAX_LOG_RETENTION` | Max number of execution logs to keep | `1000` |
| `FAASBOX_MAX_OUTPUT_SIZE` | Bytes captured per output stream, `stdout` and `stderr` alike | `1048576` |
| `FAASBOX_MAX_BODY_SIZE` | Bytes accepted in a request body | `1048576` |
| `FAASBOX_MAX_LOG_OUTPUT` | Bytes of `stdout` and `stderr` kept in each log record | `8192` |
| `FAASBOX_MAX_LOG_PAYLOAD` | Bytes of `requestPayload` kept in each log record | `4096` |
| `FAASBOX_MAX_FILE_VIEW` | Bytes a file may hold and still be shown in the **Files** tab | `262144` |

Every numeric setting behaves the same way: an absent variable uses the default silently, and a value that is unparsable, negative or zero falls back to the default with a message in the server log. A bad setting never prevents startup.

`FAASBOX_PUBLIC_URL` is the exception to the "has a default" rule, because no guessed address would be right: it is what the OAuth authorization server publishes as its own identity. Absent or malformed, the OAuth endpoints are not mounted and a startup line says why; an agent then connects with an API key, as it does today. See [09 - API Reference](09-api-reference.md#6-oauth-authorization) for the endpoints and [10 - Deployment](10-deployment.md#telling-the-instance-its-own-address) for what to put in it.

### Notes on the size limits

`FAASBOX_MAX_OUTPUT_SIZE` applies **per stream**. A single invocation can therefore hold twice that much in memory, `stdout` plus `stderr`.

`FAASBOX_MAX_FILE_VIEW` bounds **display only**. A file past it is not rendered in the Files tab, and its **Download** button still fetches it whole — raising the variable changes what you can read on screen, never what you can retrieve.

Capture limits and log limits are independent. `FAASBOX_MAX_OUTPUT_SIZE` bounds what the engine keeps and returns in the HTTP response; `FAASBOX_MAX_LOG_OUTPUT` bounds the copy written to `faasbox_logs`. Setting the log limit above the capture limit is not an error, it just means no log record is ever trimmed.

`FAASBOX_MAX_LOG_OUTPUT` can be changed on an existing database, in either direction. The `faasbox_logs` collection declares the size of its `stdout` and `stderr` fields from this value, and realigns them at every start — a raised limit is in force before the first log record is written against it. Lowering it is just as safe: the records already stored keep their full content, only what is written afterwards is trimmed.

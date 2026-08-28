# 04 - Environment Variables & Secrets

Functions often need sensitive information like API keys, database credentials, or private tokens. FaaSBox provides a secure way to manage these.

## The Problem
Storing secrets in your `index.ts` or as plain text in a database is a security risk. If someone gains access to your repository or database, your secrets are exposed.

## The Solution: AES-256-GCM Encryption
FaaSBox stores secrets **encrypted at rest**. They are only decrypted in memory just before the function is executed.

So is the rest of what you put in the instance: your function code, its `package.json`, its triggers and its execution logs are all encrypted in the database (see [06 - API Keys & Security](06-api-keys-and-security.md#what-is-encrypted-at-rest)).

### 1. The encryption key
`FAASBOX_ENCRYPTION_KEY` is **required**. The server refuses to start without it — there is nothing it could read or write.

Give it to the FaaSBox server as an environment variable.

```bash
# Generate a random 32-byte key (64 hex characters)
openssl rand -hex 32
```

Pass this to your Docker container:
```bash
-e FAASBOX_ENCRYPTION_KEY=your_64_char_hex_string
```

> ⚠️ **Warning**: this key is the instance. Lose it and the database is unreadable for good — not just the secrets, but the function code, the triggers and the history with them. Change it and the same thing happens to everything written under the old one. Back it up somewhere other than the machine that runs FaaSBox.

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
| `FAASBOX_ENCRYPTION_KEY` | 64-char hex key encrypting the database at rest | *(required)* |
| `FAASBOX_PUBLIC_URL` | The address this instance answers on, as a bare origin, `http://` or `https://` included | *(OAuth disabled)* |
| `FAASBOX_DEMOMODE` | Turns the instance into a read-only showcase | `false` |
| `FAASBOX_DEMOMODE_EMAIL` | Email the sign-in form of a demo instance shows prefilled | *(empty)* |
| `FAASBOX_DEMOMODE_PASSWORD` | Password the sign-in form of a demo instance shows prefilled | *(empty)* |
| `FAASBOX_MAX_CONCURRENCY` | Max concurrent function executions | `4` |
| `FAASBOX_MAX_LOG_RETENTION` | Max number of execution logs to keep | `1000` |
| `FAASBOX_MAX_OUTPUT_SIZE` | Bytes captured per output stream, `stdout` and `stderr` alike | `1048576` |
| `FAASBOX_MAX_BODY_SIZE` | Bytes accepted in a request body | `1048576` |
| `FAASBOX_MAX_LOG_OUTPUT` | Bytes of `stdout` and `stderr` kept in each log record | `8192` |
| `FAASBOX_MAX_LOG_PAYLOAD` | Bytes of `requestPayload` kept in each log record | `4096` |
| `FAASBOX_MAX_FILE_VIEW` | Bytes a file may hold and still be shown in the **Files** tab | `262144` |

Every numeric setting behaves the same way: an absent variable uses the default silently, and a value that is unparsable, negative or zero falls back to the default with a message in the server log. A bad numeric setting never prevents startup.

`FAASBOX_ENCRYPTION_KEY` is the exception, and the only variable of the table that is not optional: absent, malformed, or the wrong length, the server stops and says so.

`FAASBOX_DEMOMODE` is the **second** variable of the table — after `FAASBOX_ENCRYPTION_KEY` — whose unreadable value stops the server instead of falling back. The reason is what it commands: a bound that falls back costs a misconfigured limit, while `FAASBOX_DEMOMODE=treu` would leave every write route of something published as a showcase wide open. `true`, `1`, `false`, `0` and their casings are accepted; anything else is refused by name and by value. Absent, it is simply `false`.

Its two companions command nothing and are read as they stand: they fill the two fields of the sign-in form on a demo instance, and they create no account — the one they name must already exist as a superuser. See [15 - Demo Mode](15-demo-mode.md) for what the mode stops, what it publishes, and why those two variables are not simply the superuser ones.

`FAASBOX_PUBLIC_URL` is the exception to the "has a default" rule, because no guessed address would be right: it is what the OAuth authorization server publishes as its own identity. It must carry its scheme — `https://faasbox.example.com`, never `faasbox.example.com` — because an issuer without one is not an absolute URL. Absent or malformed, the OAuth endpoints are not mounted and a startup line says why; an agent then connects with an API key, as it does today. See [09 - API Reference](09-api-reference.md#6-oauth-authorization) for the endpoints and [10 - Deployment](10-deployment.md#telling-the-instance-its-own-address) for what to put in it.

### Notes on the size limits

`FAASBOX_MAX_OUTPUT_SIZE` applies **per stream**. A single invocation can therefore hold twice that much in memory, `stdout` plus `stderr`.

`FAASBOX_MAX_FILE_VIEW` bounds **display only**. A file past it is not rendered in the Files tab, and its **Download** button still fetches it whole — raising the variable changes what you can read on screen, never what you can retrieve.

Capture limits and log limits are independent. `FAASBOX_MAX_OUTPUT_SIZE` bounds what the engine keeps and returns in the HTTP response; `FAASBOX_MAX_LOG_OUTPUT` bounds the copy written to `faasbox_logs`. Setting the log limit above the capture limit is not an error, it just means no log record is ever trimmed.

`FAASBOX_MAX_LOG_OUTPUT` and `FAASBOX_MAX_LOG_PAYLOAD` can be changed on an existing database, in either direction. The `faasbox_logs` collection declares the size of the fields each one caps — `stdout` and `stderr` for the first, `requestPayload` for the second — from those values, and realigns all three at every start, so a raised limit is in force before the first log record is written against it. Lowering either is just as safe: the records already stored keep their full content, only what is written afterwards is trimmed.

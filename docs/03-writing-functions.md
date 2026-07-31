# 03 - Writing Functions

FaaSBox uses **Bun** as its runtime, which means you can write modern TypeScript or JavaScript with native support for ESM, top-level await, and npm packages.

## Naming Rules

The folder name is the function name. It must follow these rules:

- **Allowed characters**: letters (`a-z`, `A-Z`), digits (`0-9`), and hyphens (`-`).
- **Must start and end** with a letter or digit (not a hyphen).
- **Maximum length**: 64 characters.

Examples:

| Name | Valid? |
|------|--------|
| `my-function` | Yes |
| `hello123` | Yes |
| `a` | Yes |
| `-my-function` | No (starts with a hyphen) |
| `my-function-` | No (ends with a hyphen) |
| `my_function` | No (underscores not allowed) |

## Basic Structure

A function is a folder containing an `index.ts`.

```typescript
// functions/my-function/index.ts

// 1. Read input from stdin (JSON)
const input = await Bun.stdin.text();
let body = {};
try {
  body = JSON.parse(input || "{}");
} catch (e) {
  console.error("Failed to parse input:", e);
}

// 2. Business Logic
const result = {
  hello: body.name || "world",
  received: body,
};

// 3. Return output via stdout (JSON)
console.log(JSON.stringify(result));
```

## Input and Output

- **Input**: Passed as a JSON string to `stdin`. Access it via `Bun.stdin.text()`.
- **Output**: Anything written to `stdout` is captured and returned as the `result` field in the API response. We highly recommend returning a JSON string.
- **Logs**: Anything written to `stderr` (e.g., `console.error()`) is captured in the `stderr` field of the response and saved to the execution logs.

## Handling Dependencies

To use npm packages, add a `package.json` file in your function's folder.

```text
functions/
  └── use-moment/
      ├── index.ts
      └── package.json
```

**package.json example:**
```json
{
  "name": "use-moment",
  "dependencies": {
    "moment": "^2.30.1"
  }
}
```

**index.ts example:**
```typescript
import moment from "moment";

console.log(JSON.stringify({
  now: moment().format('MMMM Do YYYY, h:mm:ss a')
}));
```

### How Dependency Installation Works
- FaaSBox automatically detects the `package.json`.
- On the **first invocation**, it runs `bun install` in that directory.
- It uses a **mutex** to ensure multiple concurrent calls don't trigger multiple installs.
- It caches the `node_modules`. It only re-installs if `package.json` has a newer modification time than `node_modules`.
- **Timeout**: The installation process has a 60-second timeout.

## Testing in the Editor

The FaaSBox Editor includes a built-in **Runner** panel that lets you test your function without leaving the browser.

1. Open your function in the Editor.
2. Switch to the **Runner** tab.
3. Enter a JSON payload in the left pane (defaults to `{}`).
4. Click **Run**.

The result appears in the right pane with:
- **Status** (success or error) and execution time.
- **Result**: the parsed stdout output.
- **stdout / stderr**: raw output streams, displayed separately.

This is the fastest way to iterate on a function. You can also use `curl` for automated testing (see [06 - API Keys and Security](06-api-keys-and-security.md)).

## Best Practices

1.  **Be Stateless**: Functions should be stateless. Any persistent data should be stored in PocketBase (via its API) or an external database.
2.  **Error Handling**: Wrap your logic in `try/catch` and use `console.error` for debugging.
3.  **JSON Always**: Always try to return valid JSON. This makes it easier for the calling application to parse the result.
4.  **Keep it Small**: Smaller functions start faster and are easier to maintain.

## Technical Limits

| Feature | Limit |
|---------|-------|
| Execution Timeout | 30 seconds |
| Install Timeout | 60 seconds |
| Max Request Body | 1 MB |
| Max Stdout Capture | 10 MB |
| Max Stderr Capture | 10 MB |
| Max Stdout/Stderr Stored in Logs | 8 KB each |
| Max Request Payload Stored in Logs | 4 KB |
| Max Concurrent Executions | 4 (global) |

Capture limits and log limits are separate: the HTTP response carries the full captured output, while the copy written to `faasbox_logs` is trimmed. See [07 - Execution Logs](07-execution-logs.md).

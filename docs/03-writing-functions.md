# 03 - Writing Functions

FaaSBox uses **Bun** as its runtime, which means you can write modern TypeScript or JavaScript with native support for ESM, top-level await, and npm packages.

## Naming Rules

The folder name is the function name. It must follow these rules:

- **Allowed characters**: letters (`a-z`, `A-Z`), digits (`0-9`), and hyphens (`-`).
- **Must start and end** with a letter or digit (not a hyphen).
- **Maximum length**: 64 characters.

Examples:

| Name           | Valid?                       |
| -------------- | ---------------------------- |
| `my-function`  | Yes                          |
| `hello123`     | Yes                          |
| `a`            | Yes                          |
| `-my-function` | No (starts with a hyphen)    |
| `my-function-` | No (ends with a hyphen)      |
| `my_function`  | No (underscores not allowed) |

## Basic Structure

A function is a folder containing an `index.ts`.

```typescript
// functions/my-function/index.ts

// 1. Read input from stdin (JSON)
const payload = await Bun.stdin.text();
let body = {};
try {
  body = JSON.parse(payload || "{}");
} catch (e) {
  console.error("Failed to parse payload:", e);
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

### When the Output Is Too Big

Each stream is captured up to **1 MB** by default (see [Technical Limits](#technical-limits)). Past it, the rest is dropped and the response carries `"truncated": true`. The bound applies per stream: `stdout` and `stderr` each get their own megabyte.

> **Changed default.** The capture limit used to be 10 MB. It is now 1 MB, matching the request body limit — there was no reason to accept ten times more on the way out than on the way in. A function that returns between 1 and 10 MB now gets cut where it used to pass whole. If that is your case, raise `FAASBOX_MAX_OUTPUT_SIZE` on the server.

If your function writes JSON and the cut lands mid-document, the result is unusable: FaaSBox refuses it with a `502` instead of handing back the surviving fragment as a plain string. The error message states the effective limit and names the variable that raises it, `FAASBOX_MAX_OUTPUT_SIZE` — set it on the server and restart. Returning less data works too, and is usually the better fix.

Free-text output is not affected as long as it fits under the limit: it comes back as a raw string, exactly as before.

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

console.log(
  JSON.stringify({
    now: moment().format("MMMM Do YYYY, h:mm:ss a"),
  }),
);
```

### How Dependency Installation Works

- FaaSBox automatically detects the `package.json`.
- **Saving** your function runs `bun install` in its directory, in the background. The save returns immediately — you do not wait for the install.
- The corollary matters: an edited `package.json` that has not been saved installs **nothing**. The dependency is on your screen and nowhere else, which is why the editor shows a banner until you save.
- It caches the `node_modules` and only re-installs when the dependency spec changed. Editing your script alone never triggers an install.
- When it does re-install, it **starts from an empty `node_modules`**: the old one is deleted first. This is what makes a dependency you removed from your `package.json` actually go away — `bun` on its own updates the lockfile but leaves the package folder behind, still importable, until the next restart on a fresh machine breaks the import for you.
- The flip side: **a failed install leaves your function with no dependencies at all.** It used to keep running against the previous `node_modules`; it no longer does. A typo in a package name, once saved, breaks the function — HTTP calls and cron runs alike — until you fix the `package.json`. Fixing it and saving starts a clean install and puts the function back.
- If an invocation arrives before the install is done, it waits for it rather than failing.
- **After a restart**, FaaSBox reinstalls what the machine lost on its own, in the background, one function at a time. The server answers straight away — the pass does not hold it up — and the editor shows each function go from `installing` to `ready` while it runs.
- An invocation still installs on its own when needed, as a fallback: a `node_modules` removed by hand, or a startup install that failed. In that case, and only then, the caller waits for the install.
- **Timeout**: The installation process has a 60-second timeout. Past it the install is stopped and reported as a timeout, in those words — raise nothing, split your dependencies or drop the heaviest one. The 60 seconds cover `bun install` itself: a call that had to queue behind another install of the same function is not charged for the wait.

### Your Versions Stay Put

`"^1.11.0"` accepts any 1.x release, so the same `package.json` could install a different version tomorrow. It does not: the first install records the exact versions it resolved, and every install after that reuses them.

The pinning **survives a redeployment**. FaaSBox keeps it with your function rather than on the machine, so a restart on a fresh filesystem reinstalls the versions you have been running, not whatever is newest.

A version range is only re-resolved when you **change your `package.json`**. Adding a dependency resolves that one and leaves the others exactly where they were.

There is nothing to do and nothing to look at: no file to commit, no file to edit, no setting. The flip side is that a function stays on its versions until you touch its `package.json` — including for a patch release you would have wanted. Editing the `package.json` and saving is how you ask for a refresh.

### Following the Installation

The **package.json** tab of the editor shows where the install stands, right above the code. It updates **on its own**, without reloading the page: the server pushes each change as it happens, so a save shows the install start, run and finish while you watch. A function with no `package.json` shows nothing at all.

A failed install is shown there in full, with the `bun` output preformatted in a scrollable block — the same text the record carries.

The same information lives on the function record, in two fields:

| `depsStatus` | Meaning                                                                                   |
| ------------ | ----------------------------------------------------------------------------------------- |
| _(empty)_    | The function declares no `package.json`.                                                  |
| `pending`    | The install has been requested, or was interrupted before it could finish and is still owed. |
| `installing` | `bun install` is running.                                                                 |
| `ready`      | `node_modules` matches the current dependencies.                                          |
| `error`      | The last install failed — `depsError` says why.                                           |

Both fields are written whichever way the install was triggered: by saving the function, by the startup pass, or by an invocation that had to install on its own. A `ready` left behind by a restart on a fresh filesystem is therefore corrected within seconds of the server coming back, without waiting for anyone to call the function.

An install that never got to finish — the server shut down, or the HTTP call that triggered it was abandoned — goes back to `pending`, never to `error`. Nothing is wrong with the dependencies in that case: the work is simply still owed, and the next call or the next startup does it.

`depsError` opens on one of three things:

- **`dependency install timed out`** — the 60 seconds ran out.
- **`dependency install was killed by the system…`** — something outside stopped the process, most often the machine running out of memory. Nothing can say which for certain: a killed process leaves no note.
- Anything else — `bun`'s own output, which names the package or the lockfile at fault.

The output kept is the **end** of the install, not its beginning: `bun` prints its progress first and its failure last. A long install that overflows the field opens on a `...[truncated, N bytes total]` marker saying how much was dropped ahead of what you are reading.

Both fields are also readable from the PocketBase admin UI, in the `faasbox_functions` collection — useful when you want the raw value rather than the editor's rendering.

## What the Function's Folder Holds

Every function has a folder of its own on the server, named after it. FaaSBox writes into it: `index.ts` and `package.json` on every save, `bun.lock` once an install has resolved your versions, and `node_modules/` once `bun install` has run.

The **Files** tab of the editor browses that folder as it is on disk right now. That is its whole point: the other tabs show what the database holds, this one shows what the machine actually has. It is where you look when an install behaved strangely, when you want to know which version of a package really landed, or when you want to read a file out of `node_modules` without opening a shell in the container — which, on an ephemeral host, you may not be able to do at all.

Navigation is what you expect: the current path is shown at the top, a button goes back to the parent folder, and each level lists its folders first and its files after, both by name. A folder line carries how many entries it holds, `node_modules` included — a number worth knowing before you click into it. Clicking a file shows it on the right, with a **Download** button.

The tab is **read-only**. It has no delete, no rename, no upload. Emptying `node_modules` is not something you ask for here; saving a `package.json` is what triggers an install. Editing the managed files stays where it belongs: `index.ts` in the **Script** tab, `package.json` in its own.

Two files are shown differently:

- A **binary** file is not printed. The verdict is a NUL byte in its first kilobytes, not its extension — `node_modules/.bin` is full of files with no extension at all. Download it to inspect it.
- A file **larger than 256 KB** (`FAASBOX_MAX_FILE_VIEW`) is not printed either, and the message says how big it is. The limit is on what gets displayed, never on what gets downloaded: the **Download** button works whatever the size.

> **Your working directory is the functions root, not your own folder.** A function that writes to a relative path — `Bun.write("cache.json", …)` — creates the file next to its folder, not inside it, so the Files tab will not show it. Build the path from `import.meta.dir`, which is your own folder, if you want the file to land where the tab looks. And remember that nothing written to disk survives a redeployment: only what the database holds is restored.

## Testing in the Editor

The FaaSBox Editor includes a built-in **Runner** panel that lets you test your function without leaving the browser.

1. Open your function in the Editor.
2. Open the **Runner** panel from the header.
3. Enter a JSON payload in the left pane (defaults to `{}`).
4. Click **Run**, or **Save and run** if you changed something.

The result appears in the right pane with:

- **Status** (success or error) and execution time.
- **Result**: the parsed stdout output.
- **stdout / stderr**: raw output streams, displayed separately.

### Run and Save and run

**Run** is always there. It executes what the server holds: the Runner calls the same `/invoke/{name}` endpoint as any other caller, and that endpoint runs the file on disk. Nothing is saved, so what you get is your last saved version.

**Save and run** only appears while what is on your screen differs from what is saved, and it disappears as soon as the save goes through. It writes the name, the script and the `package.json`, then invokes. That is the button to use to test what you just typed — without saving, `/invoke` would never see it. If your `package.json` changed, saving starts the dependency install and the run waits for it to finish rather than executing against stale dependencies.

The rule of thumb: if **Save and run** is on screen, **Run** is about to test something other than what you are looking at.

Nothing is executed if the save itself fails — the server refusing the record, for instance. Fix what the message reports and click again. Your environment variables are not part of this: the **Environment** tab has its own **Save**, and neither **Save** nor **Save and run** touches your secrets.

This is the fastest way to iterate on a function. You can also use `curl` for automated testing (see [06 - API Keys and Security](06-api-keys-and-security.md)).

## Best Practices

1.  **Be Stateless**: Functions should be stateless. Any persistent data should be stored in PocketBase (via its API) or an external database.
2.  **Error Handling**: Wrap your logic in `try/catch` and use `console.error` for debugging.
3.  **JSON Always**: Always try to return valid JSON. This makes it easier for the calling application to parse the result.
4.  **Keep it Small**: Smaller functions start faster and are easier to maintain.

## Technical Limits

| Feature                            | Limit                                 |
| ---------------------------------- | ------------------------------------- |
| Execution Timeout                  | 30 seconds                            |
| Install Timeout                    | 60 seconds                            |
| Max Request Body                   | 1 MB (`FAASBOX_MAX_BODY_SIZE`)        |
| Max Stdout Capture                 | 1 MB (`FAASBOX_MAX_OUTPUT_SIZE`)      |
| Max Stderr Capture                 | 1 MB (`FAASBOX_MAX_OUTPUT_SIZE`)      |
| Max Stdout/Stderr Stored in Logs   | 8 KB each (`FAASBOX_MAX_LOG_OUTPUT`)  |
| Max Request Payload Stored in Logs | 4 KB (`FAASBOX_MAX_LOG_PAYLOAD`)      |
| Max Concurrent Executions          | 4 (global, `FAASBOX_MAX_CONCURRENCY`) |
| Max File Shown in the Files Tab    | 256 KB (`FAASBOX_MAX_FILE_VIEW`)      |

Every limit that names a variable is a **default**, not a hard ceiling: set the variable on the server and restart. See [04 - Environment Variables](04-environment-variables.md).

Capture limits and log limits are separate: the HTTP response carries the full captured output, while the copy written to `faasbox_logs` is trimmed. See [07 - Execution Logs](07-execution-logs.md).

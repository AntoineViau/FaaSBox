# 07 - Execution Logs

Monitoring and auditing are crucial for any FaaS platform. FaaSBox automatically records every execution in the `faasbox_logs` collection.

## What is Recorded?

Each log entry contains comprehensive details about a function run:

| Field | Description |
|-------|-------------|
| `function` | The function the entry belongs to. Filter and group on this one. |
| `functionName` | The name that function carried **when it ran** — see below. |
| `trigger` | `http` (API call), `cron` (scheduled task), `startup` (fired when the server came up) or `mcp` (an [AI agent](13-ai-agents.md) called it). |
| `status` | `success`, `error`, `timeout`, or `missed`. |
| `duration` | Total execution time in milliseconds. |
| `stdout` | The output of the function (captured from stdout), truncated to 8 KB. |
| `stderr` | Debug logs or error messages (captured from stderr), truncated to 8 KB. |
| `requestPayload` | The [envelope](03-writing-functions.md#the-input-envelope) the run received, truncated to 4 KB. |
| `exitCode` | The process exit code (0 usually means success). |
| `truncated` | `true` when at least one of the three fields above was cut to fit this record. |

### Two Ways to Say "Which Function"

A log entry carries both, and they answer different questions.

`function` points at the function itself. It is what the history is keyed on, so **renaming a function keeps its whole past attached to it** — the Logs panel and any filter you write keep returning everything, from before and after the change.

`functionName` is a **trace**: the name in force at the moment the entry was written. It is deliberately not refreshed. A log is an account of something that happened, and reading `stderr: cannot reach the API` next to a name the function no longer answers to is the truth about that run, not a stale value.

So an old entry can show a name the editor no longer uses. That is the intended reading. Filter on `function` when you want a history; read `functionName` when you want to know what things were called at the time.

**Deleting a function deletes its logs**, in the same operation. Nothing survives it. If you need the history of a function you are about to remove, export it first — the retention purge is not the only thing that can take it away.

### The Envelope, Not Just the Payload

`requestPayload` holds the whole envelope handed to the function — the trigger, and on an HTTP call the method, path, query and headers as well. A log entry therefore says **how** a run was called, not only with what, which is what you need when one caller out of ten is sending something the others are not.

Two consequences:

- **The four headers FaaSBox refuses to forward are not stored either.** `x-api-key`, `authorization`, `cookie` and `proxy-authorization` never enter the envelope, so they never reach this collection. Every other header does — including anything custom your callers send, so treat this column as you would any request log.
- **Entries are bigger than they used to be**, and more of them hit the 4 KB cap below. That is the price of the context; raise `FAASBOX_MAX_LOG_PAYLOAD` if your callers send headers worth keeping.

## Output Truncation

Stored logs are capped: **8 KB** per output stream (`FAASBOX_MAX_LOG_OUTPUT`) and **4 KB** for the envelope (`FAASBOX_MAX_LOG_PAYLOAD`). Beyond the cap, the value is cut and a marker stating the original size is appended:

```
...[truncated, 5242880 bytes total]
```

The cut always falls on a character boundary, so the stored value stays valid UTF-8.

### The `truncated` Flag

The marker tells a human reading one entry that something is missing. The `truncated` boolean answers the other question: **which runs had their output cut?** Filter or count on it — a marker buried in the body of a text field can do neither.

It is `true` as soon as **any one** of `stdout`, `stderr` or `requestPayload` was cut, and `false` when all three fit. It does not say which one; if you need that, compare the field lengths against the caps.

> **It reports the cut made when writing the record, not the one made during the run.** These are two different events, and an output can hit both: capped at 1 MB while the function was running (`FAASBOX_MAX_OUTPUT_SIZE`), then trimmed to 8 KB on its way into the log. The `truncated` field of a log record only ever means the second. The `truncated` field of an [invocation response](09-api-reference.md) only ever means the first. An output of 10 KB is the common case where they disagree: it survived the run whole, so the response says `false`, and it was trimmed for storage, so the log says `true`.

One consequence to know if you read logs programmatically: a truncated `requestPayload` is no longer valid JSON, so it is stored as an escaped **string** instead of an object. An envelope under 4 KB keeps its original shape. Code consuming the logs — through [Read a Function's Logs](09-api-reference.md#4-read-a-functions-logs) or the PocketBase Records API — should handle both.

Truncation applies **only to the persisted copy**. The HTTP response of `POST /invoke/{name}` still carries the full captured output — that is where you read it while debugging. Once the response is gone, the trimmed part is lost, which matters most for cron functions whose response nobody reads: keep diagnostic output short enough to survive in the logs.

## Integrated Log Viewer

The FaaSBox Editor includes a built-in **Logs** panel for each function:

1. Open your function in the Editor.
2. Click **Logs** in the header — it toggles a panel below the code, alongside the **Runner**.
3. Logs appear in real time — new entries are pushed automatically as the function runs.

Each entry shows the status (success, error, timeout, missed), trigger type (http/cron/startup/mcp), duration, and relative timestamp. Click on any entry to expand it and see the full stdout, stderr, envelope, and exit code.

The log viewer displays the 50 most recent entries for the current function. Use the refresh button to reload the list.

## From the API

`GET /api/faasbox/functions/{idOrName}/logs` returns the same history to a script or an agent, with an API key carrying `canManage` — no superuser token. See [Read a Function's Logs](09-api-reference.md#4-read-a-functions-logs) for the contract, the `limit` parameter and the codes.

That endpoint matters most for functions **nobody is waiting on** — a cron or a startup trigger: such a run answers no HTTP response, so its log entry is the only account of what it printed and why it failed.

## From the Admin UI (optional)

You can also view logs in the **PocketBase Admin UI** under the `faasbox_logs` collection.
- **Filter** by `status="error"` to find failing functions.
- **Filter** by `truncated=true` to find the runs whose stored output is incomplete — a function that shows up there constantly is one to make quieter.
- **Filter** by `function` to see the full history of one function, renames included. Filtering by `functionName` splits it at every rename.
- **Sort** by `created` to see the most recent runs.

Reaching the collection this way needs a superuser token, which is full power over the instance. Prefer the endpoint above for anything scripted.

### Not the `Logs` Section

The admin UI has a `Logs` entry of its own, and it is **not** this. That one is PocketBase's internal record of HTTP requests — one line per call, with its URL, status, duration, IP and user agent — and it knows nothing about what your functions printed. You will see your `/invoke/{name}` calls listed there, but their output is only in `faasbox_logs`.

Two things follow. It lives in a separate file, `pb_data/auxiliary.db`, so [Litestream replication](10-deployment.md#3-litestream-replication-optional) does not carry it and a restored instance starts with it empty. And unlike your execution history it is not encrypted, so it holds those `/invoke/{name}` URLs — function names included — as plain text on the instance disk. Its retention is set in that same `Logs` view, under the `Logs settings` button — five days by default, `0` to stop recording requests entirely. FaaSBox never changes it, and no environment variable does either.

## Debugging with Stderr

When writing your functions, use `console.error()` for any information that is helpful for debugging but shouldn't be part of the final result. 

```typescript
console.error(`Processing order ${orderId}...`);
// ... logic
console.log(JSON.stringify({ status: "processed" }));
```

In the logs, the "Processing order..." message will appear in the `stderr` field, keeping the `stdout` clean for the actual JSON result.

## Log Pruning (Maintenance)

To prevent the database from growing indefinitely, FaaSBox includes an automatic pruning mechanism.
- An internal cron job runs **every hour** to delete the oldest logs beyond the retention limit.
- By default, only the **last 1000 entries** are kept in the `faasbox_logs` collection.
- Configure the retention limit with the `FAASBOX_MAX_LOG_RETENTION` environment variable (see [04 - Environment Variables](04-environment-variables.md)).

## Missed Cron Runs

A `missed` entry is not an execution: it reports scheduled runs that never happened because the server was stopped when they were due. It is written at startup, one per affected cron trigger, with `trigger: "cron"`, a `duration` of 0, and a `stderr` message stating how many triggers were lost and over which period. See [05 - Triggers](05-triggers.md) for the full behaviour.

Filter on `status="missed"` in the Admin UI to review downtime impact.

## Error States

- **timeout**: The function exceeded the 30-second limit.
- **error**: The process exited with a non-zero exit code or failed to start.
- **success**: The process exited with code 0.
- **missed**: Scheduled runs were due while the server was down. No function was executed.

# 07 - Execution Logs

Monitoring and auditing are crucial for any FaaS platform. FaaSBox automatically records every execution in the `faasbox_logs` collection.

## What is Recorded?

Each log entry contains comprehensive details about a function run:

| Field | Description |
|-------|-------------|
| `functionName` | The name of the function that was called. |
| `trigger` | `http` (API call) or `cron` (scheduled task). |
| `status` | `success`, `error`, `timeout`, or `missed`. |
| `duration` | Total execution time in milliseconds. |
| `stdout` | The output of the function (captured from stdout), truncated to 8 KB. |
| `stderr` | Debug logs or error messages (captured from stderr), truncated to 8 KB. |
| `requestPayload` | The JSON input sent to the function, truncated to 4 KB. |
| `exitCode` | The process exit code (0 usually means success). |
| `truncated` | `true` when at least one of the three fields above was cut to fit this record. |

## Output Truncation

Stored logs are capped: **8 KB** per output stream (`FAASBOX_MAX_LOG_OUTPUT`) and **4 KB** for the request payload (`FAASBOX_MAX_LOG_PAYLOAD`). Beyond the cap, the value is cut and a marker stating the original size is appended:

```
...[truncated, 5242880 bytes total]
```

The cut always falls on a character boundary, so the stored value stays valid UTF-8.

### The `truncated` Flag

The marker tells a human reading one entry that something is missing. The `truncated` boolean answers the other question: **which runs had their output cut?** Filter or count on it — a marker buried in the body of a text field can do neither.

It is `true` as soon as **any one** of `stdout`, `stderr` or `requestPayload` was cut, and `false` when all three fit. It does not say which one; if you need that, compare the field lengths against the caps.

> **It reports the cut made when writing the record, not the one made during the run.** These are two different events, and an output can hit both: capped at 1 MB while the function was running (`FAASBOX_MAX_OUTPUT_SIZE`), then trimmed to 8 KB on its way into the log. The `truncated` field of a log record only ever means the second. The `truncated` field of an [invocation response](09-api-reference.md) only ever means the first. An output of 10 KB is the common case where they disagree: it survived the run whole, so the response says `false`, and it was trimmed for storage, so the log says `true`.

One consequence to know if you read logs programmatically: a truncated `requestPayload` is no longer valid JSON, so it is stored as an escaped **string** instead of an object. A payload under 4 KB keeps its original shape. Code consuming `faasbox_logs` through the [PocketBase Records API](09-api-reference.md) should handle both.

Truncation applies **only to the persisted copy**. The HTTP response of `POST /invoke/{name}` still carries the full captured output — that is where you read it while debugging. Once the response is gone, the trimmed part is lost, which matters most for cron functions whose response nobody reads: keep diagnostic output short enough to survive in the logs.

## Integrated Log Viewer

The FaaSBox Editor includes a built-in **Logs** panel for each function:

1. Open your function in the Editor.
2. Switch to the **Logs** tab.
3. Logs appear in real time — new entries are pushed automatically as the function runs.

Each entry shows the status (success, error, timeout, missed), trigger type (http/cron), duration, and relative timestamp. Click on any entry to expand it and see the full stdout, stderr, payload, and exit code.

The log viewer displays the 50 most recent entries for the current function. Use the refresh button to reload the list.

## Viewing Logs (Admin UI)

You can also view logs in the **PocketBase Admin UI** under the `faasbox_logs` collection.
- **Filter** by `status="error"` to find failing functions.
- **Filter** by `truncated=true` to find the runs whose stored output is incomplete — a function that shows up there constantly is one to make quieter.
- **Search** by `functionName` to see the history of a specific function.
- **Sort** by `created` to see the most recent runs.

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

A `missed` entry is not an execution: it reports scheduled runs that never happened because the server was stopped when they were due. It is written at startup, one per affected cron job, with `trigger: "cron"`, a `duration` of 0, and a `stderr` message stating how many triggers were lost and over which period. See [05 - Scheduling (Cron)](05-scheduling-cron.md) for the full behaviour.

Filter on `status="missed"` in the Admin UI to review downtime impact.

## Error States

- **timeout**: The function exceeded the 30-second limit.
- **error**: The process exited with a non-zero exit code or failed to start.
- **success**: The process exited with code 0.
- **missed**: Scheduled runs were due while the server was down. No function was executed.

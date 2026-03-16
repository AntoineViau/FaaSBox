# 07 - Execution Logs

Monitoring and auditing are crucial for any FaaS platform. FaaSBox automatically records every execution in the `faasbox_logs` collection.

## What is Recorded?

Each log entry contains comprehensive details about a function run:

| Field | Description |
|-------|-------------|
| `functionName` | The name of the function that was called. |
| `trigger` | `http` (API call) or `cron` (scheduled task). |
| `status` | `success`, `error`, or `timeout`. |
| `duration` | Total execution time in milliseconds. |
| `stdout` | The full output of the function (captured from stdout). |
| `stderr` | Debug logs or error messages (captured from stderr). |
| `requestPayload` | The JSON input sent to the function. |
| `exitCode` | The process exit code (0 usually means success). |

## Integrated Log Viewer

The FaaSBox Editor includes a built-in **Logs** panel for each function:

1. Open your function in the Editor.
2. Switch to the **Logs** tab.
3. Logs appear in real time — new entries are pushed automatically as the function runs.

Each entry shows the status (success, error, timeout), trigger type (http/cron), duration, and relative timestamp. Click on any entry to expand it and see the full stdout, stderr, payload, and exit code.

The log viewer displays the 50 most recent entries for the current function. Use the refresh button to reload the list.

## Viewing Logs (Admin UI)

You can also view logs in the **PocketBase Admin UI** under the `faasbox_logs` collection.
- **Filter** by `status="error"` to find failing functions.
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

## Error States

- **timeout**: The function exceeded the 30-second limit.
- **error**: The process exited with a non-zero exit code or failed to start.
- **success**: The process exited with code 0.

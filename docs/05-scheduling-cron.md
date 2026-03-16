# 05 - Scheduling (Cron)

FaaSBox includes a powerful scheduling system that allows you to run functions at specific intervals.

## How it Works
FaaSBox leverages PocketBase's internal cron scheduler. It monitors the `faasbox_cron_jobs` collection and dynamically registers or updates cron tasks.

## Scheduling from the Editor

The easiest way to manage cron jobs is directly from the FaaSBox Editor:

1. Open your function in the Editor.
2. Switch to the **Triggers** tab.
3. Click **Add trigger**.
4. Configure the schedule (pick a preset or type a custom cron expression), payload, and active state.
5. Changes are saved automatically.

You can toggle triggers on/off, edit their schedule and payload, or delete them — all from this panel. The scheduler picks up changes in real time, no restart needed.

## Creating a Scheduled Task (Admin UI)

Alternatively, you can manage cron jobs from the PocketBase Admin UI:

1.  Open the **PocketBase Admin UI**.
2.  Go to the **faasbox_cron_jobs** collection.
3.  Create a new record with the following fields:
    - **Name**: A descriptive name (e.g., "Daily Cleanup").
    - **Schedule**: A standard cron expression (see below).
    - **FunctionName**: The name of the function to execute.
    - **Payload**: A JSON string to be passed as input to the function.
    - **Active**: Toggle this to `true` to enable the task.
    - **MaxQueue** *(optional)*: Maximum number of simultaneous executions (waiting + running) allowed for this cron job. When the limit is reached, subsequent triggers are skipped with a warning in the logs. Set to `0` or leave empty for no limit (default).

## Cron Syntax

FaaSBox uses the standard 5-column cron syntax:

```text
┌───────────── minute (0 - 59)
│ ┌───────────── hour (0 - 23)
│ │ ┌───────────── day of the month (1 - 31)
│ │ │ ┌───────────── month (1 - 12)
│ │ │ │ ┌───────────── day of the week (0 - 6) (Sunday to Saturday)
│ │ │ │ │
│ │ │ │ │
* * * * *
```

### Common Examples:
- `* * * * *`: Every minute.
- `*/5 * * * *`: Every 5 minutes.
- `0 * * * *`: Every hour (at the start of the hour).
- `0 0 * * *`: Every day at midnight.
- `0 0 * * 1`: Every Monday at midnight.

## Live Updates
The scheduler is "hot-reloaded". You don't need to restart the server when you add, modify, or delete a cron job in the database. 
- **Creating/Enabling**: The task starts immediately according to the schedule.
- **Modifying**: The old schedule is canceled and the new one is applied instantly.
- **Deleting/Disabling**: The task is removed from the scheduler immediately.

## Monitoring Cron Jobs
Every time a cron job runs, it creates an entry in the **faasbox_logs** collection with the `trigger` set to `cron`. You can see the success/failure status and the output of the scheduled run there.

## Best Practices for Cron Functions
1.  **Timeout Awareness**: Cron jobs have the same 30-second timeout as HTTP invocations. If your task takes longer, consider breaking it into smaller chunks or optimizing it.
2.  **Idempotency**: Ensure your function can be run multiple times without causing side effects if a previous run failed or was interrupted.
3.  **Logging**: Use `console.error` generously in cron jobs so you can debug issues via the execution logs.

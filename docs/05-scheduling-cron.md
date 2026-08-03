# 05 - Scheduling (Cron)

FaaSBox includes a powerful scheduling system that allows you to run functions at specific intervals.

## How it Works
FaaSBox leverages PocketBase's internal cron scheduler. It monitors the `faasbox_cron_jobs` collection and dynamically registers or updates cron tasks.

## Scheduling from the Editor

The easiest way to manage cron jobs is directly from the FaaSBox Editor:

1. Open your function in the Editor.
2. Switch to the **Triggers** tab.
3. Click **Add trigger**. The row appears on screen — nothing is written yet.
4. Configure the schedule (click an example in the **Cron syntax** fold, or type your own expression), payload, and active state.
5. Open the **Advanced** fold if you need to cap concurrency — see **Max queue** below.
6. Click **Save**.

You can toggle triggers on/off, edit their schedule, payload and max queue, or delete them — all from this panel. Once saved, the scheduler picks the changes up in real time, no restart needed.

### Nothing Is Written Before You Save

Adding, editing and deleting a trigger all stay on screen until you click **Save**, which writes the whole list at once. The button is disabled while nothing has changed.

This matters because the server can refuse a trigger — an invalid cron expression is the usual case. When it does, the panel names the trigger and reports why, the row keeps what you typed so you can fix it, and the other triggers are still saved. Click **Save** again once corrected.

An invalid JSON payload is the one exception: it stops the save before anything is written at all, rather than saving part of the list.

### The Cron Syntax Fold

Under the schedule field, the **Cron syntax** fold — closed by default — spells out the five columns and lists ready-made expressions. Click one and it fills the field.

### Max queue

Every trigger carries a **Max queue** setting. It sits behind the **Advanced** fold, closed by default, because most schedules never need it.

It caps how many executions of that trigger may exist at the same time — waiting plus running. Once the cap is reached, further triggers are skipped and a warning is written to the server log.

This matters when a function can take longer to finish than the gap between two triggers. Without a cap, the runs pile up and eat the shared concurrency slots, starving your other functions. With a max queue of `1`, a run still going when the next one is due simply gives up its turn.

Set it to `0` — the default — for no limit. Empty and negative values are read as `0`.

A skipped run is not the same as a missed run: skipping is the behaviour you asked for, while a missed run means the trigger never fired at all because the server was down (see below).

## From the Admin UI (optional)

You never need this — the Triggers tab covers everything below. It is here for when you are already in the collections, or scripting against them:

1.  Open the **PocketBase Admin UI**.
2.  Go to the **faasbox_cron_jobs** collection.
3.  Create a new record with the following fields:
    - **Name**: A descriptive name (e.g., "Daily Cleanup").
    - **Schedule**: A standard cron expression (see below).
    - **FunctionName**: The name of the function to execute.
    - **Payload**: A JSON string to be passed as input to the function.
    - **Active**: Toggle this to `true` to enable the task.
    - **MaxQueue** *(optional)*: Maximum number of simultaneous executions (waiting + running) allowed for this cron job. Same setting as **Max queue** in the Editor, described above. Set to `0` or leave empty for no limit (default).

The collection also carries a **LastRunAt** field. It is written by the server after each run and used to detect missed runs (see below) — leave it alone.

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

Each run also stamps the `lastRunAt` field of the cron job record, whatever the outcome — a failed or timed-out run still counts as a run, because what the stamp records is that the trigger fired.

## When the Server Is Down

The scheduler lives inside the FaaSBox process. While the server is stopped — redeployment, maintenance, crash, host restart — **no cron job fires, and nothing is queued for later**. Triggers due during that window are simply lost.

FaaSBox does not replay them, but it does tell you about them. On every startup, each active cron job is checked against the period since its `lastRunAt` (or since its creation date, for a job that never ran):

- a warning is written to the application log;
- a single entry with `status: "missed"` and `trigger: "cron"` is added to `faasbox_logs`, carrying the number of occurrences and the period concerned.

You get **one entry per job and per startup**, never one per lost occurrence — a job scheduled every minute over a two-day outage would otherwise write thousands of rows and flush the rest of your logs.

The count looks back **30 days** at most. Past that bound the entry reports how many runs were missed inside that window, states that older occurrences were not counted, and still tells you when the job last ran — so you get both "how bad since the cap" and "silent since when".

One consequence of that bound: a schedule whose period is as long as the window or longer — monthly, yearly, or February 29th — may report **nothing at all** after a very long outage, because none of its occurrences fall inside the window that is actually walked. FaaSBox stays silent rather than claiming a miss it never observed. Anything **weekly or more frequent** is always reported, since its period is shorter than the window.

Because the missed runs are never replayed, a job whose work must not be skipped should be written to catch up on its own — for instance by processing everything pending since the last successful run rather than only the current period.

## Best Practices for Cron Functions
1.  **Timeout Awareness**: Cron jobs have the same 30-second timeout as HTTP invocations. If your task takes longer, consider breaking it into smaller chunks or optimizing it.
2.  **Idempotency**: Ensure your function can be run multiple times without causing side effects if a previous run failed or was interrupted.
3.  **Logging**: Use `console.error` generously in cron jobs so you can debug issues via the execution logs.

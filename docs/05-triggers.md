# 05 - Triggers

A function runs when something asks it to. An HTTP call is one way — see [09 - API Reference](09-api-reference.md#1-invoke-a-function). A **trigger** is the other: the server fires the function itself, on a deadline you declare, and nobody is waiting on the other end.

A trigger comes in two kinds. A **cron** trigger carries a five-field expression and fires on schedule. A **startup** trigger carries no expression and fires once when the server comes up, after a delay you choose. Everything below applies to both unless it says otherwise — see [Running at Startup](#running-at-startup).

## How it Works

Triggers live in the `faasbox_triggers` collection. FaaSBox reads it at startup and whenever a record changes: cron triggers are handed to PocketBase's internal scheduler, and startup triggers get a timer of their own.

## Adding a Trigger from the Editor

The easiest way to manage triggers is directly from the FaaSBox Editor:

1. Open your function in the Editor.
2. Switch to the **Triggers** tab.
3. Click **Add trigger**. The row appears on screen — nothing is written yet.
4. Configure the schedule (click an example in the **Cron syntax** fold, or type your own expression), payload, and active state. To run the function when the server comes up instead, tick **Startup trigger** — the schedule field gives way to a delay.
5. Open the **Advanced** fold if you need to cap concurrency — see **Max queue** below.
6. Click **Save**.

You can toggle triggers on/off, edit their schedule, payload and max queue, or delete them — all from this panel. Once saved, the scheduler picks the changes up in real time, no restart needed.

### A Trigger Follows Its Function

A trigger points at the function itself, not at its name. Two consequences, both of them things you no longer have to think about:

- **Renaming a function leaves its triggers running.** They keep firing on schedule, and the log entries they write carry the new name. Nothing to re-point, nothing to re-save.
- **Deleting a function deletes its triggers.** They go with it, in the same operation. There is no such thing as a leftover schedule firing into the void.

### Nothing Is Written Before You Save

Adding, editing and deleting a trigger all stay on screen until you click **Save**, which writes the whole list at once. The button is disabled while nothing has changed.

This matters because the server can refuse a trigger — an invalid cron expression is the usual case. When it does, the panel names the trigger and reports why, the row keeps what you typed so you can fix it, and the other triggers are still saved. Click **Save** again once corrected.

An invalid JSON payload is the one exception: it stops the save before anything is written at all, rather than saving part of the list.

Because nothing is written until then, switching to another function while the panel holds unsaved triggers asks you to confirm first — the rows are dropped when the panel reloads for the function you picked.

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
2.  Go to the **faasbox_triggers** collection.
3.  Create a new record with the following fields:
    - **Name**: A descriptive name (e.g., "Daily Cleanup").
    - **Schedule**: A standard cron expression (see below).
    - **Function**: the function to execute, picked from the `faasbox_functions` collection. It is a relation and it is required — a trigger with nothing to fire has no reason to exist.
    - **Payload**: A JSON string to be passed as input to the function.
    - **Active**: Toggle this to `true` to enable the task.
    - **MaxQueue** *(optional)*: Maximum number of simultaneous executions (waiting + running) allowed for this trigger. Same setting as **Max queue** in the Editor, described above. Set to `0` or leave empty for no limit (default).

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
The cron scheduler is "hot-reloaded". You don't need to restart the server when you add, modify, or delete a cron trigger in the database. Startup triggers are the exception — see [Running at Startup](#running-at-startup). 
- **Creating/Enabling**: The task starts immediately according to the schedule.
- **Modifying**: The old schedule is canceled and the new one is applied instantly.
- **Deleting/Disabling**: The task is removed from the scheduler immediately.

## Running at Startup

A cron expression cannot say "when the server comes up". A `0 3 * * *` fires at three in the morning whether the box was restarted at noon or never; and a schedule that runs every minute to test a marker burns 1440 runs a day for an event that happens once per redeployment.

A **startup trigger** is that event. Tick **Startup trigger** on a trigger row and the schedule field is replaced by **Run this long after startup**: two fields, hours and minutes. The function then runs once, that long after the server has come up, and again at every restart.

The delay matters as much as the event. A box comes up before the rest of your infrastructure necessarily does, so a function that calls a third-party service, warms a cache from another host, or checks external state should let things settle first. Leave the delay at `0h00` to fire as soon as the server is up.

At most **23h59**. Past a day it is no longer a startup trigger but a schedule, and cron carries that better.

Three things to know:

- **A startup trigger is not hot-reloaded.** Creating one, editing it or switching it on while the server is running fires nothing. It is armed at boot and only at boot, so it waits for the next start. That is the nature of the deadline, not a limitation — the cron triggers next to it are still picked up in real time.
- **The same holds the other way round: switching one off does not disarm it.** A trigger already armed and still counting down its delay fires anyway, even if you untick it or delete it in the meantime — what was armed at boot runs for that boot. Deleting the **function** does stop it, since a run resolves its function when it fires. To be sure a startup trigger will not run, untick it and restart.
- **It fires again at every restart**, including every redeployment. That is the point, and it is also the trap: a startup function that brings the box down will run again the moment it comes back up, and again after that. Nothing on the server side breaks that loop — write the function so a failure is survivable, and test it before ticking the box.
- **Several startup triggers at `0h00` all queue at once** on the shared concurrency slots. That is the normal behaviour for a trigger nobody is waiting on — the run waits its turn rather than being refused — but **Max queue** applies here too, and staggering the delays is usually simpler.

A startup run is a run like any other: it writes an entry to `faasbox_logs` with `trigger` set to `startup`, and it stamps `lastRunAt` on the trigger record.

## Monitoring Triggers
Every time a trigger fires, it creates an entry in the **faasbox_logs** collection with the `trigger` set to `cron` or `startup`. You can see the success/failure status and the output of the run there.

Each run also stamps the `lastRunAt` field of the trigger record, whatever the outcome — a failed or timed-out run still counts as a run, because what the stamp records is that the trigger fired.

## When the Server Is Down

The scheduler lives inside the FaaSBox process. While the server is stopped — redeployment, maintenance, crash, host restart — **no cron trigger fires, and nothing is queued for later**. Triggers due during that window are simply lost.

This is about cron triggers only: a startup trigger cannot be missed, since the event it waits for is the server coming back. It never produces a `missed` entry.

FaaSBox does not replay them, but it does tell you about them. On every startup, each active cron trigger is checked against the period since its `lastRunAt` (or since its creation date, for one that never ran):

- a warning is written to the application log;
- a single entry with `status: "missed"` and `trigger: "cron"` is added to `faasbox_logs`, carrying the number of occurrences and the period concerned.

You get **one entry per trigger and per startup**, never one per lost occurrence — a trigger scheduled every minute over a two-day outage would otherwise write thousands of rows and flush the rest of your logs.

The count looks back **30 days** at most. Past that bound the entry reports how many runs were missed inside that window, states that older occurrences were not counted, and still tells you when the trigger last ran — so you get both "how bad since the cap" and "silent since when".

One consequence of that bound: a schedule whose period is as long as the window or longer — monthly, yearly, or February 29th — may report **nothing at all** after a very long outage, because none of its occurrences fall inside the window that is actually walked. FaaSBox stays silent rather than claiming a miss it never observed. Anything **weekly or more frequent** is always reported, since its period is shorter than the window.

Because the missed runs are never replayed, a trigger whose work must not be skipped should be written to catch up on its own — for instance by processing everything pending since the last successful run rather than only the current period.

## Best Practices for Triggered Functions
1.  **Timeout Awareness**: A triggered run has the same 30-second timeout as an HTTP invocation. If your task takes longer, consider breaking it into smaller chunks or optimizing it.
2.  **Idempotency**: Ensure your function can be run multiple times without causing side effects if a previous run failed or was interrupted.
3.  **Logging**: Use `console.error` generously in triggered functions so you can debug issues via the execution logs.

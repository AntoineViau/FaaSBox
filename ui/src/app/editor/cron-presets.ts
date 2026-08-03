/**
 * Ready-made cron expressions.
 *
 * Single list on purpose: the label shown under the schedule field and the
 * clickable examples of the syntax help both read it. A second table would
 * diverge without anything ever failing.
 */
export const CRON_PRESETS: Record<string, string> = {
  '* * * * *': 'Every minute',
  '*/5 * * * *': 'Every 5 minutes',
  '*/10 * * * *': 'Every 10 minutes',
  '*/15 * * * *': 'Every 15 minutes',
  '*/30 * * * *': 'Every 30 minutes',
  '0 * * * *': 'Every hour',
  '0 */2 * * *': 'Every 2 hours',
  '0 */6 * * *': 'Every 6 hours',
  '0 */12 * * *': 'Every 12 hours',
  '0 0 * * *': 'Every day at midnight',
  '0 6 * * *': 'Every day at 6:00 AM',
  '0 12 * * *': 'Every day at noon',
  '0 0 * * 0': 'Every Sunday at midnight',
  '0 0 * * 1': 'Every Monday at midnight',
  '0 0 1 * *': 'First day of every month',
};

/** The same table as a list, for whatever has to render every entry. */
export const CRON_PRESET_LIST: ReadonlyArray<{ expression: string; label: string }> =
  Object.entries(CRON_PRESETS).map(([expression, label]) => ({ expression, label }));

/**
 * Reads an expression back to the user. Not a validation: the server is the
 * only judge of what a schedule is worth.
 */
export function describeSchedule(schedule: string): string {
  const trimmed = schedule.trim();
  if (!trimmed) return '';
  return CRON_PRESETS[trimmed] ?? 'Custom schedule — check crontab.guru';
}

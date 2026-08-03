#!/usr/bin/env node
/**
 * Fills a throwaway FaaSBox instance with the demo content the screenshots
 * show: a handful of believable functions, their schedules, their secrets,
 * and enough log rows for the viewer to look lived-in.
 *
 *   node seed.mjs [--url http://127.0.0.1:8099]
 *
 * Never point this at an instance you care about: it only ever adds records,
 * but they are demo records.
 */

const flag = (name, fallback) => {
  const i = process.argv.indexOf(`--${name}`);
  return i === -1 ? fallback : process.argv[i + 1];
};

const BASE = flag('url', 'http://127.0.0.1:8099');
const EMAIL = flag('email', 'shots@faasbox.local');
const PASSWORD = flag('password', 'shotspassword123');

const auth = await (await fetch(`${BASE}/api/collections/_superusers/auth-with-password`, {
  method: 'POST',
  headers: { 'content-type': 'application/json' },
  body: JSON.stringify({ identity: EMAIL, password: PASSWORD }),
})).json();

if (!auth.token) throw new Error(`could not authenticate as ${EMAIL}: ${JSON.stringify(auth)}`);

const H = { 'content-type': 'application/json', authorization: auth.token };

async function create(collection, body, label) {
  const res = await fetch(`${BASE}/api/collections/${collection}/records`, {
    method: 'POST', headers: H, body: JSON.stringify(body),
  });
  console.log(res.ok ? `  ${label}` : `  ✗ ${label}: ${await res.text()}`);
  return res.ok;
}

/* -------------------------------------------------------------------------- */
/* Functions                                                                   */
/* -------------------------------------------------------------------------- */

/* summarise-events is the one the capture opens: it is the only function here
   that completes without network access, so the runner shows a real success
   rather than a connection error. */
const FUNCTIONS = [
  {
    name: 'summarise-events',
    script: `const { events } = await Bun.stdin.json();

const byDay = {};

for (const event of events) {
  const day = event.at.slice(0, 10);
  byDay[day] ??= { deploys: 0, rollbacks: 0, actors: new Set() };
  byDay[day][event.type === 'rollback' ? 'rollbacks' : 'deploys']++;
  byDay[day].actors.add(event.actor);
}

const summary = Object.fromEntries(
  Object.entries(byDay).map(([day, d]) => [
    day,
    { deploys: d.deploys, rollbacks: d.rollbacks, actors: d.actors.size },
  ]),
);

console.log(JSON.stringify({ days: Object.keys(summary).length, summary }));
`,
    packageJson: '',
    plainEnv: { REPORT_TZ: 'Europe/Paris' },
  },
  {
    name: 'daily-report',
    script: `import { Resend } from 'resend';

const { team, since } = await Bun.stdin.json();

const res = await fetch(\`\${process.env.API_BASE}/teams/\${team}/activity?since=\${since}\`, {
  headers: { authorization: \`Bearer \${process.env.API_TOKEN}\` },
});

const { events } = await res.json();
const shipped = events.filter((e) => e.type === 'deploy');

const resend = new Resend(process.env.RESEND_KEY);
await resend.emails.send({
  from: 'reports@example.com',
  to: process.env.REPORT_TO,
  subject: \`\${team}: \${shipped.length} deploys since \${since}\`,
  text: shipped.map((e) => \`- \${e.actor} shipped \${e.service}\`).join('\\n'),
});

console.log(JSON.stringify({ sent: shipped.length, team }));
`,
    packageJson: JSON.stringify({ name: 'daily-report', dependencies: { resend: '^4.0.0' } }, null, 2),
    plainEnv: {
      API_BASE: 'https://api.internal.example.com',
      API_TOKEN: 'sk_live_8f2a91c4e7b6d3f0a5c8',
      RESEND_KEY: 're_Kp9mQxN2_7dLvTfR4sWz',
      REPORT_TO: 'platform@example.com',
    },
  },
  {
    name: 'morning-digest',
    script: `const feeds = process.env.FEEDS.split(',');

const items = (
  await Promise.all(
    feeds.map(async (url) => {
      const res = await fetch(url, { signal: AbortSignal.timeout(5000) });
      return res.ok ? (await res.json()).items ?? [] : [];
    }),
  )
).flat();

const fresh = items
  .filter((i) => Date.parse(i.published) > Date.now() - 864e5)
  .sort((a, b) => Date.parse(b.published) - Date.parse(a.published))
  .slice(0, 20);

console.log(JSON.stringify({ count: fresh.length, items: fresh }));
`,
    packageJson: '',
    plainEnv: { FEEDS: 'https://news.example.com/feed.json,https://blog.example.org/feed.json' },
  },
  {
    name: 'rotate-keys',
    script: `const { dryRun = false } = await Bun.stdin.json();

const res = await fetch(\`\${process.env.VAULT_URL}/keys?age_gt=90d\`, {
  headers: { 'x-vault-token': process.env.VAULT_TOKEN },
});

const stale = await res.json();
const rotated = [];

for (const key of stale) {
  if (!dryRun) {
    await fetch(\`\${process.env.VAULT_URL}/keys/\${key.id}/rotate\`, {
      method: 'POST',
      headers: { 'x-vault-token': process.env.VAULT_TOKEN },
    });
  }
  rotated.push(key.name);
}

console.log(JSON.stringify({ rotated, dryRun }));
`,
    packageJson: '',
    plainEnv: { VAULT_URL: 'https://vault.internal.example.com/v1', VAULT_TOKEN: 'hvs.CAESIJx8Qm2pLd9' },
  },
  {
    name: 'stripe-webhook',
    script: `const event = await Bun.stdin.json();

if (event.type !== 'invoice.payment_failed') {
  console.log(JSON.stringify({ ignored: event.type }));
  process.exit(0);
}

const { customer_email, amount_due } = event.data.object;

await fetch(process.env.SLACK_WEBHOOK, {
  method: 'POST',
  headers: { 'content-type': 'application/json' },
  body: JSON.stringify({
    text: \`Payment failed: \${customer_email} — \${(amount_due / 100).toFixed(2)} EUR\`,
  }),
});

console.log(JSON.stringify({ notified: customer_email }));
`,
    packageJson: '',
    plainEnv: { SLACK_WEBHOOK: 'https://hooks.slack.com/services/T00/B00/XXXXXXXXXXXX' },
  },
  {
    name: 'prune-uploads',
    script: `const { keepDays = 30 } = await Bun.stdin.json();

const cutoff = Date.now() - keepDays * 864e5;
const res = await fetch(\`\${process.env.STORAGE_API}/objects?prefix=tmp/\`);
const { objects } = await res.json();

const stale = objects.filter((o) => Date.parse(o.modified) < cutoff);

for (const object of stale) {
  await fetch(\`\${process.env.STORAGE_API}/objects/\${object.key}\`, { method: 'DELETE' });
}

console.log(JSON.stringify({ deleted: stale.length, keepDays }));
`,
    packageJson: '',
    plainEnv: { STORAGE_API: 'https://s3.internal.example.com/api' },
  },
];

for (const fn of FUNCTIONS) {
  await create('faasbox_functions', { ...fn, plainEnv: JSON.stringify(fn.plainEnv) }, `fn ${fn.name}`);
}

/* -------------------------------------------------------------------------- */
/* Schedules                                                                   */
/* -------------------------------------------------------------------------- */

const CRONS = [
  { name: 'Daily platform report', schedule: '0 7 * * 1-5', functionName: 'daily-report', payload: JSON.stringify({ team: 'platform', since: '24h' }), active: true, maxQueue: 1 },
  { name: 'Morning digest', schedule: '*/15 * * * *', functionName: 'morning-digest', payload: '{}', active: true },
  { name: 'Rotate stale keys', schedule: '0 3 1 * *', functionName: 'rotate-keys', payload: JSON.stringify({ dryRun: false }), active: true },
  { name: 'Prune temporary uploads', schedule: '30 2 * * 0', functionName: 'prune-uploads', payload: JSON.stringify({ keepDays: 30 }), active: false },
];

for (const cron of CRONS) {
  await create('faasbox_cron_jobs', cron, `cron ${cron.name}`);
}

/* -------------------------------------------------------------------------- */
/* Logs                                                                        */
/* -------------------------------------------------------------------------- */

/* The log viewer filters by function, so the one on screen needs its own rows.
   `created` is an autodate the server overwrites, hence every row reading
   "just now" — harmless here, and the only way around it is raw SQL. */
const SUMMARY = '{"days":2,"summary":{"2026-08-01":{"deploys":2,"rollbacks":1,"actors":2},"2026-08-02":{"deploys":1,"rollbacks":0,"actors":1}}}';

const LOGS = [
  ['summarise-events', 'http', 'success', 34, SUMMARY, '', '{"events":[…4 items]}', 0],
  ['summarise-events', 'cron', 'success', 41, '{"days":7,"summary":{…}}', '', '{"events":[…31 items]}', 0],
  ['summarise-events', 'http', 'success', 29, SUMMARY, '', '{"events":[…4 items]}', 0],
  ['summarise-events', 'cron', 'success', 38, '{"days":7,"summary":{…}}', '', '{"events":[…28 items]}', 0],
  ['summarise-events', 'http', 'error', 22, '', 'SyntaxError: Unexpected end of JSON input', '{"events":', 1],
  ['summarise-events', 'cron', 'success', 44, '{"days":7,"summary":{…}}', '', '{"events":[…35 items]}', 0],
  ['morning-digest', 'cron', 'success', 412, '{"count":18,"items":[…]}', '', '{}', 0],
  ['daily-report', 'cron', 'success', 1840, '{"sent":14,"team":"platform"}', '', '{"team":"platform","since":"24h"}', 0],
  ['stripe-webhook', 'http', 'success', 236, '{"notified":"acme@example.com"}', '', '{"type":"invoice.payment_failed"}', 0],
  ['prune-uploads', 'cron', 'success', 6120, '{"deleted":248,"keepDays":30}', '', '{"keepDays":30}', 0],
  ['rotate-keys', 'cron', 'timeout', 30000, '', 'execution exceeded 30s', '{"dryRun":false}', -1],
  ['prune-uploads', 'cron', 'missed', 0, '', '', '{"keepDays":30}', 0],
];

let logged = 0;
for (const [functionName, trigger, status, duration, stdout, stderr, requestPayload, exitCode] of LOGS) {
  const res = await fetch(`${BASE}/api/collections/faasbox_logs/records`, {
    method: 'POST', headers: H,
    body: JSON.stringify({ functionName, trigger, status, duration, stdout, stderr, requestPayload, exitCode, truncated: false }),
  });
  if (res.ok) logged++;
  else console.error(`  ✗ log ${functionName}: ${await res.text()}`);
}
console.log(`  ${logged} log rows`);

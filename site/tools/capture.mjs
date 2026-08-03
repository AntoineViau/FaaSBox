#!/usr/bin/env node
/**
 * Drives headless Chrome over the DevTools Protocol to photograph the editor,
 * once per theme, and installs the results in ../assets/shots/.
 *
 *   node capture.mjs [--url http://127.0.0.1:8099]
 *
 * Node 23 ships a global WebSocket, so the protocol side needs no
 * dependencies. ImageMagick's `convert` is used to downscale and index the
 * PNGs; without it the full-size captures are written as they come.
 */

import { spawn } from 'node:child_process';
import { writeFile, mkdir, rm, readdir, rename } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
const SHOTS = join(HERE, '..', 'assets', 'shots');
const WORK = join(HERE, '.capture');

const flag = (name, fallback) => {
  const i = process.argv.indexOf(`--${name}`);
  return i === -1 ? fallback : process.argv[i + 1];
};

const APP = flag('url', 'http://127.0.0.1:8099');
const EMAIL = flag('email', 'shots@faasbox.local');
const PASSWORD = flag('password', 'shotspassword123');
const PORT = Number(flag('cdp-port', 9333));

const VIEWPORT = { width: 1440, height: 900 };
/* Captured at 2×, then halved on the way out: the editor is a max-width
   layout, so the useful area is narrower than the window. */
const SCALE = 2;
const OUTPUT_WIDTH = 2280;

/** What gets photographed. Add an entry to capture more. */
const TARGETS = [
  { name: 'editor-full', selector: 'div.mx-auto.flex.h-screen' },
];

/** The payload typed into the runner before it is run. */
const PAYLOAD = JSON.stringify({
  events: [
    { at: '2026-08-01T09:14:00Z', type: 'deploy', actor: 'marion' },
    { at: '2026-08-01T16:02:00Z', type: 'deploy', actor: 'sam' },
    { at: '2026-08-01T17:41:00Z', type: 'rollback', actor: 'sam' },
    { at: '2026-08-02T10:25:00Z', type: 'deploy', actor: 'marion' },
  ],
}, null, 2);

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/* -------------------------------------------------------------------------- */
/* Chrome, over the DevTools Protocol                                          */
/* -------------------------------------------------------------------------- */

const auth = await (await fetch(`${APP}/api/collections/_superusers/auth-with-password`, {
  method: 'POST',
  headers: { 'content-type': 'application/json' },
  body: JSON.stringify({ identity: EMAIL, password: PASSWORD }),
})).json();

if (!auth.token) throw new Error(`could not authenticate as ${EMAIL} on ${APP}`);

await rm(WORK, { recursive: true, force: true });
await mkdir(WORK, { recursive: true });
await mkdir(SHOTS, { recursive: true });

const chrome = spawn('google-chrome', [
  '--headless=new',
  `--remote-debugging-port=${PORT}`,
  '--no-sandbox',
  '--disable-gpu',
  '--hide-scrollbars',
  `--window-size=${VIEWPORT.width},${VIEWPORT.height}`,
  `--user-data-dir=${join(WORK, 'profile')}`,
  'about:blank',
], { stdio: 'ignore' });

async function debuggerUrl() {
  for (let i = 0; i < 50; i++) {
    try {
      const list = await (await fetch(`http://127.0.0.1:${PORT}/json/list`)).json();
      const page = list.find((t) => t.type === 'page');
      if (page) return page.webSocketDebuggerUrl;
    } catch { /* not up yet */ }
    await sleep(200);
  }
  throw new Error('Chrome did not expose a page target');
}

const ws = new WebSocket(await debuggerUrl());
await new Promise((resolve) => ws.addEventListener('open', resolve, { once: true }));

let seq = 0;
const waiting = new Map();
ws.addEventListener('message', (event) => {
  const msg = JSON.parse(event.data);
  const pending = waiting.get(msg.id);
  if (!pending) return;
  waiting.delete(msg.id);
  msg.error ? pending.reject(new Error(msg.error.message)) : pending.resolve(msg.result);
});

function send(method, params = {}) {
  const id = ++seq;
  ws.send(JSON.stringify({ id, method, params }));
  return new Promise((resolve, reject) => waiting.set(id, { resolve, reject }));
}

async function evaluate(expression) {
  const { result, exceptionDetails } = await send('Runtime.evaluate', {
    expression, awaitPromise: true, returnByValue: true,
  });
  if (exceptionDetails) throw new Error(`${exceptionDetails.text} — ${expression.slice(0, 80)}`);
  return result.value;
}

/** Polls an expression until it is truthy. */
async function until(expression, label, tries = 60) {
  for (let i = 0; i < tries; i++) {
    if (await evaluate(expression)) return;
    await sleep(250);
  }
  throw new Error(`timed out waiting for ${label}`);
}

await send('Page.enable');
await send('Runtime.enable');
await send('Emulation.setDeviceMetricsOverride', {
  ...VIEWPORT, deviceScaleFactor: SCALE, mobile: false,
});

/* -------------------------------------------------------------------------- */
/* Capture                                                                     */
/* -------------------------------------------------------------------------- */

async function shoot(name, selector) {
  const box = await evaluate(`(() => {
    const el = document.querySelector(${JSON.stringify(selector)});
    if (!el) return null;
    const r = el.getBoundingClientRect();
    return { x: r.x, y: r.y, width: r.width, height: r.height };
  })()`);

  if (!box || box.width < 2 || box.height < 2) throw new Error(`no box for ${name} (${selector})`);

  const { data } = await send('Page.captureScreenshot', {
    format: 'png',
    clip: { ...box, scale: SCALE },
    captureBeyondViewport: true,
  });

  const file = join(WORK, `${name}.png`);
  await writeFile(file, Buffer.from(data, 'base64'));
  return file;
}

async function capture(theme) {
  /* Seed auth and theme before the app boots, so it never shows /login. */
  await send('Page.addScriptToEvaluateOnNewDocument', {
    source: `
      localStorage.setItem('faasbox_token', ${JSON.stringify(auth.token)});
      localStorage.setItem('faasbox-theme', ${JSON.stringify(theme)});
    `,
  });

  await send('Page.navigate', { url: `${APP}/editor` });
  await until("!!document.querySelector('app-sidebar')", 'sidebar');
  await until(
    "[...document.querySelectorAll('app-sidebar div.cursor-pointer')].some(d => d.textContent.includes('summarise-events'))",
    'function list',
  );

  /* Function rows are clickable divs, not buttons — the buttons are the
     per-row delete icons. The runner is open by default, so nothing to click. */
  await evaluate(`
    [...document.querySelectorAll('app-sidebar div.cursor-pointer')]
      .find((d) => d.textContent.includes('summarise-events'))?.click()
  `);
  await until("!!document.querySelector('z-tab-group')", 'tabs');
  await until("!!document.querySelector('app-runner')", 'runner');
  await sleep(900);

  /* The payload field is CodeMirror, not a textarea — setting .value does
     nothing. Focus it, select all, and type over it through real key events. */
  await evaluate(`document.querySelector('app-runner .cm-content')?.focus(), true`);
  await sleep(200);
  for (const type of ['keyDown', 'keyUp']) {
    await send('Input.dispatchKeyEvent', {
      type, modifiers: 2, key: 'a', code: 'KeyA', windowsVirtualKeyCode: 65,
    });
  }
  await send('Input.insertText', { text: PAYLOAD });
  await sleep(400);

  await evaluate(`
    [...document.querySelectorAll('app-runner button')]
      .find((b) => b.textContent.trim() === 'Run')?.click()
  `);
  await until("!!document.querySelector('app-runner pre')", 'run result');
  await sleep(700);

  const suffix = theme === 'dark' ? '-dark' : '';
  for (const { name, selector } of TARGETS) {
    const file = await shoot(`${name}${suffix}`, selector);
    console.log(`  ${theme}: ${name}${suffix}.png`);
    await install(file, `${name}${suffix}.png`);
  }
}

/* -------------------------------------------------------------------------- */
/* Install                                                                     */
/* -------------------------------------------------------------------------- */

/** Downscales and indexes the PNG into assets/shots/, or moves it as-is. */
function install(file, name) {
  const target = join(SHOTS, name);
  return new Promise((resolve) => {
    const convert = spawn('convert', [
      file, '-resize', `${OUTPUT_WIDTH}x`, '-colors', '256', '-strip', `PNG8:${target}`,
    ], { stdio: 'ignore' });

    convert.on('error', async () => {
      console.warn('    convert not found — installing the full-size capture');
      await rename(file, target);
      resolve();
    });
    convert.on('exit', (code) => {
      if (code !== 0) console.warn(`    convert exited ${code} for ${name}`);
      resolve();
    });
  });
}

for (const theme of ['light', 'dark']) await capture(theme);

ws.close();

/* Wait for Chrome to actually exit: it keeps writing to its profile for a
   moment after kill(), and removing the directory under it fails. */
const exited = new Promise((resolve) => chrome.once('exit', resolve));
chrome.kill();
await Promise.race([exited, sleep(5000)]);
await rm(WORK, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });

console.log(`\n  installed in assets/shots/: ${(await readdir(SHOTS)).filter((f) => f.endsWith('.png')).join(', ')}`);

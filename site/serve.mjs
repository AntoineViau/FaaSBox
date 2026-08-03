#!/usr/bin/env node
/**
 * Local preview server for the site. Builds once, serves dist/ over HTTP, and
 * rebuilds whenever a source file changes — reload the tab to see it.
 *
 *   node serve.mjs [--port 8000]
 *
 * Serving over HTTP rather than opening dist/index.html directly matters:
 * on file:// the language links resolve to a directory listing and the
 * clipboard API is unavailable.
 *
 * No dependencies. Node 18+.
 */

import { createServer } from 'node:http';
import { watch } from 'node:fs';
import { readFile, stat } from 'node:fs/promises';
import { join, resolve, extname, sep } from 'node:path';

import { build, DIST, ROOT } from './build.mjs';

const portFlag = process.argv.indexOf('--port');
const PORT = portFlag === -1 ? 8000 : Number(process.argv[portFlag + 1]) || 8000;

const TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.webp': 'image/webp',
  '.xml': 'application/xml; charset=utf-8',
  '.ico': 'image/x-icon',
};

/* -------------------------------------------------------------------------- */
/* Build                                                                       */
/* -------------------------------------------------------------------------- */

async function rebuild(reason) {
  const started = Date.now();
  try {
    const problems = await build({ quiet: true });
    problems.forEach((p) => console.error(`  ✗ ${p}`));
    const state = problems.length ? 'with holes' : 'ok';
    console.log(`  ${reason} → rebuilt ${state} in ${Date.now() - started}ms`);
  } catch (error) {
    console.error(`  ✗ ${reason} → build failed: ${error.message}`);
  }
}

/* Editors write a file in several bursts; collapse them into one rebuild. */
let pending = null;
function scheduleRebuild(reason) {
  clearTimeout(pending);
  pending = setTimeout(() => rebuild(reason), 80);
}

/* -------------------------------------------------------------------------- */
/* Serve                                                                       */
/* -------------------------------------------------------------------------- */

/** Maps a URL path to a file inside dist/, or null if it escapes it. */
function locate(urlPath) {
  const decoded = decodeURIComponent(urlPath.split('?')[0]);
  const target = resolve(DIST, '.' + decoded);
  if (target !== DIST && !target.startsWith(DIST + sep)) return null;
  return target;
}

const server = createServer(async (req, res) => {
  const target = locate(req.url);

  if (!target) {
    res.writeHead(403).end('Forbidden');
    return;
  }

  let file = target;
  try {
    if ((await stat(file)).isDirectory()) {
      /* /fr → /fr/, so relative asset paths resolve one level up as intended. */
      if (!req.url.endsWith('/')) {
        res.writeHead(301, { location: req.url + '/' }).end();
        return;
      }
      file = join(file, 'index.html');
    }
  } catch {
    /* Missing path: fall through to the read below and answer 404. */
  }

  try {
    const body = await readFile(file);
    res.writeHead(200, {
      'content-type': TYPES[extname(file)] || 'application/octet-stream',
      'cache-control': 'no-store',
    });
    res.end(body);
  } catch {
    res.writeHead(404, { 'content-type': 'text/plain; charset=utf-8' });
    res.end(`404 — ${req.url}\n`);
  }
});

/* -------------------------------------------------------------------------- */
/* Start                                                                       */
/* -------------------------------------------------------------------------- */

await rebuild('initial build');

/* Editors and sed drop temporary files next to the real ones; ignore them. */
const WATCHED = { content: /\.json$/, assets: /\.(css|js|svg|png|jpg|webp|ico)$/ };

for (const [dir, pattern] of Object.entries(WATCHED)) {
  watch(join(ROOT, dir), { recursive: true }, (_event, name) => {
    if (name && pattern.test(name)) scheduleRebuild(`${dir}/${name}`);
  });
}
watch(join(ROOT, 'template.html'), () => scheduleRebuild('template.html'));

server.listen(PORT, () => {
  console.log(`\n  http://localhost:${PORT}/        English`);
  console.log(`  http://localhost:${PORT}/fr/     Français`);
  console.log(`\n  Watching template, content and assets. Ctrl-C to stop.\n`);
});

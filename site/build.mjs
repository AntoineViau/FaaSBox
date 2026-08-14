#!/usr/bin/env node
/**
 * Renders template.html once per language file in content/, into dist/.
 *
 * The default language lands at dist/index.html, the others at dist/<lang>/.
 * Each page is complete HTML — no client-side text swapping — so both
 * languages are indexable and share correctly.
 *
 *   node build.mjs
 *
 * No dependencies. Node 18+.
 */

import { readFile, writeFile, mkdir, readdir, rm, cp } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const ROOT = dirname(fileURLToPath(import.meta.url));
const DIST = join(ROOT, 'dist');

/** Absolute base of the published site, used for canonical, hreflang and CNAME. */
const SITE = 'https://community.faasbox.net/';
const REPO = 'https://github.com/AntoineViau/FaaSBox';
const DOCS = `${REPO}/blob/master/docs/`;
const DEFAULT_LANG = 'en';

/* -------------------------------------------------------------------------- */
/* Helpers                                                                     */
/* -------------------------------------------------------------------------- */

/** Resolves "how.steps.0.title" against the content object. */
function get(content, path) {
  return path.split('.').reduce((node, key) => (node == null ? node : node[key]), content);
}

/** Documentation links point at the repo; docs/ is not part of this site. */
function docHref(doc) {
  return doc.startsWith('../') ? `${REPO}/blob/master/${doc.slice(3)}` : DOCS + doc;
}

function pageUrl(lang) {
  return lang === DEFAULT_LANG ? SITE : `${SITE}${lang}/`;
}

/** Parses "1600 × 1000" into [width, height]. Only used before a file exists. */
function declaredSize(size) {
  const [w, h] = String(size ?? '').split(/\s*[×x]\s*/).map(Number);
  return Number.isFinite(w) && Number.isFinite(h) ? [w, h] : [16, 10];
}

/** The dark counterpart of a capture: name.png → name-dark.png. */
function darkVariant(src) {
  return src.replace(/(\.[a-z0-9]+)$/i, '-dark$1');
}

/**
 * A screenshot slot. Renders the real <img> once the file exists under
 * assets/, and a labelled placeholder holding its aspect ratio until then —
 * so dropping a capture in is the only step needed to publish it.
 *
 * When a `-dark` counterpart exists, both are emitted and the stylesheet shows
 * the one matching the active theme. Both carry loading="lazy", so the hidden
 * one is never fetched.
 */
function shotFrame(shot, className, ctx) {
  const dark = darkVariant(shot.src);
  const hasDark = ctx.captures.has(dark);

  if (ctx.captures.has(shot.src)) {
    const img = (src, extra) => {
      const [w, h] = ctx.sizes.get(src) ?? declaredSize(shot.size);
      return `<img class="${className}${extra}" src="${ctx.base}${src}" alt="${shot.alt}" width="${w}" height="${h}" loading="lazy" decoding="async">`;
    };
    return hasDark
      ? img(shot.src, ' shot--light') + '\n        ' + img(dark, ' shot--dark')
      : img(shot.src, '');
  }

  /* The ratio travels as a custom property, not as aspect-ratio directly, so a
     stylesheet rule can still override it — the sidebar slot stretches instead. */
  const [w, h] = declaredSize(shot.size);
  return `<div class="${className} shot--pending" style="--ratio: ${w} / ${h}">
            <span class="shot__path">${shot.src}</span>
            <span class="shot__size">${shot.size}</span>
          </div>`;
}

/** A plain list of pre-written items: the values already carry their markup. */
function listItems(items) {
  return items.map((i) => `<li>${i}</li>`).join('\n        ');
}

/** Reads a PNG's intrinsic size from its IHDR chunk. */
function pngSize(buffer) {
  const png = buffer.length > 24 && buffer.readUInt32BE(0) === 0x89504e47;
  return png ? [buffer.readUInt32BE(16), buffer.readUInt32BE(20)] : null;
}

/* -------------------------------------------------------------------------- */
/* Renderers — one per repeating block in the template                         */
/* -------------------------------------------------------------------------- */

const renderers = {
  hreflang: (c, ctx) =>
    [
      ...ctx.languages.map((l) => `<link rel="alternate" hreflang="${l}" href="${pageUrl(l)}">`),
      `<link rel="alternate" hreflang="x-default" href="${pageUrl(DEFAULT_LANG)}">`,
    ].join('\n'),

  /* Each option carries its own URL, so the switcher scales to any number of
     languages and site.js needs no routing rules of its own. */
  langOptions: (c, ctx) =>
    ctx.languages
      .map((l) => {
        const url = l === DEFAULT_LANG ? ctx.base || './' : `${ctx.base}${l}/`;
        const selected = l === c.lang ? ' selected' : '';
        return `<option value="${url}" lang="${l}"${selected}>${ctx.labels[l]}</option>`;
      })
      .join('\n          '),

  heroShot: (c, ctx) => shotFrame(c.hero.shot, 'shot__media', ctx),

  /* The four rows of step one are written out in the template rather than
     looped, so each keeps its own capture; these fill their media cells. */
  shotScript: (c, ctx) =>
    shotFrame({ src: 'assets/shots/tab-script.png', alt: c.how.steps[0].bullets[0].alt },
      'bullet__media', ctx),

  shotPackage: (c, ctx) =>
    shotFrame({ src: 'assets/shots/tab-package.png', alt: c.how.steps[0].bullets[1].alt },
      'bullet__media', ctx),

  shotRunner: (c, ctx) =>
    shotFrame({ src: 'assets/shots/runner-run.png', alt: c.how.steps[0].bullets[2].alt },
      'bullet__media', ctx),

  shotFiles: (c, ctx) =>
    shotFrame({ src: 'assets/shots/tab-files.png', alt: c.how.steps[0].bullets[3].alt },
      'bullet__media', ctx),

  shotKeys: (c, ctx) =>
    shotFrame({ src: 'assets/shots/keys-page.png', alt: c.how.steps[1].ways[0].alt },
      'bullet__media', ctx),

  shotTriggers: (c, ctx) =>
    shotFrame({ src: 'assets/shots/tab-triggers.png', alt: c.how.steps[1].ways[1].alt },
      'bullet__media', ctx),

  cards: (c) =>
    c.inside.cards
      .map(
        (card) => `<article class="card">
            <h3>${card.title}</h3>
            <p>${card.body}</p>
          </article>`,
      )
      .join('\n          '),

  agentPoints: (c) => listItems(c.agents.points),

  scopeItems: (c) => listItems(c.scope.items),

  footerLinks: (c) =>
    c.footer.links
      .map((l) => `<a href="${docHref(l.doc)}">${l.text}</a>`)
      .join('\n      '),
};

/* -------------------------------------------------------------------------- */
/* Render                                                                      */
/* -------------------------------------------------------------------------- */

function render(template, content, ctx) {
  const missing = [];

  const html = template.replace(/\{\{(@?[\w.]+)\}\}/g, (match, key) => {
    if (key.startsWith('@')) {
      const renderer = renderers[key.slice(1)];
      if (!renderer) { missing.push(match); return ''; }
      return renderer(content, ctx);
    }
    if (key in ctx) return ctx[key];

    const value = get(content, key);
    if (value === undefined) { missing.push(match); return ''; }
    return value;
  });

  return { html, missing };
}

/* -------------------------------------------------------------------------- */
/* Build                                                                       */
/* -------------------------------------------------------------------------- */

/**
 * Renders every language into dist/. Returns the list of unresolved
 * placeholders — empty means the build is complete.
 */
export async function build({ quiet = false } = {}) {
  const say = quiet ? () => {} : console.log;

  const template = await readFile(join(ROOT, 'template.html'), 'utf8');

  const files = (await readdir(join(ROOT, 'content'))).filter((f) => f.endsWith('.json')).sort();
  const contents = await Promise.all(
    files.map(async (f) => JSON.parse(await readFile(join(ROOT, 'content', f), 'utf8'))),
  );

  /* Default language first, so it reads as the primary one in the switcher. */
  const languages = contents
    .map((c) => c.lang)
    .sort((a, b) => (a === DEFAULT_LANG ? -1 : b === DEFAULT_LANG ? 1 : a.localeCompare(b)));

  const labels = Object.fromEntries(contents.map((c) => [c.lang, c.label]));

  /* Which captures are actually on disk — the rest render as placeholders —
     and their true pixel size, so width/height never drift from the file. */
  const captures = new Set();
  const sizes = new Map();

  for (const file of await readdir(join(ROOT, 'assets', 'shots'))) {
    if (file.startsWith('.')) continue;
    const src = `assets/shots/${file}`;
    captures.add(src);
    const size = pngSize(await readFile(join(ROOT, 'assets', 'shots', file)));
    if (size) sizes.set(src, size);
  }

  await rm(DIST, { recursive: true, force: true });
  await mkdir(DIST, { recursive: true });

  const problems = [];

  for (const content of contents) {
    const isDefault = content.lang === DEFAULT_LANG;
    const ctx = {
      languages,
      labels,
      captures,
      sizes,
      base: isDefault ? '' : '../',
      repo: REPO,
      docs: DOCS,
      canonical: pageUrl(content.lang),
      gettingStarted: `${DOCS}02-getting-started.md`,
      introduction: `${DOCS}00-introduction.md`,
    };

    const { html, missing } = render(template, content, ctx);

    if (missing.length) {
      problems.push(`${content.lang}: unresolved ${[...new Set(missing)].join(', ')}`);
    }

    const out = isDefault ? join(DIST, 'index.html') : join(DIST, content.lang, 'index.html');
    await mkdir(dirname(out), { recursive: true });
    await writeFile(out, html);
    say(`  ✓ ${out.slice(ROOT.length + 1)}`);
  }

  await cp(join(ROOT, 'assets'), join(DIST, 'assets'), { recursive: true });
  await writeFile(join(DIST, '.nojekyll'), '');

  /* Pages reads the custom domain from the artifact; without it the setting can
     be dropped on a later deployment. Derived from SITE so the two never drift. */
  const domain = new URL(SITE).hostname;
  await writeFile(join(DIST, 'CNAME'), `${domain}\n`);

  const sitemap = `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
${languages.map((l) => `  <url><loc>${pageUrl(l)}</loc></url>`).join('\n')}
</urlset>
`;
  await writeFile(join(DIST, 'sitemap.xml'), sitemap);

  say(`  ✓ assets, sitemap.xml, .nojekyll, CNAME (${domain})`);

  return problems;
}

export { DIST, ROOT };

/* Run as a script — but stay importable by serve.mjs. */
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const problems = await build();
  if (problems.length) {
    problems.forEach((p) => console.error(`  ✗ ${p}`));
    console.error('\nBuild finished with unresolved placeholders.');
    process.exit(1);
  }
}

# FaaSBox website

The marketing site, published to GitHub Pages by `.github/workflows/pages.yml`
on every push to `master` that touches this folder.

```
build.mjs           renders template.html once per language, into dist/
serve.mjs           local preview, rebuilds on save
template.html       page structure — {{keys}} resolved from a content file
content/en.json     every English string
content/fr.json     every French string
assets/             stylesheet, script, favicon, screenshots
tools/              regenerates the screenshots from a running FaaSBox
dist/               generated, git-ignored, never edited by hand
```

## Preview it locally

```bash
node serve.mjs                 # http://localhost:8000, rebuilds on save
node serve.mjs --port 4000
```

It builds, serves `dist/` and watches `template.html`, `content/` and `assets/`.
Save a file, reload the tab. Use this rather than opening `dist/index.html`
directly: on `file://` the language links land on a directory listing instead of
the page, and the clipboard API is unavailable outside a secure context.

## Build without serving

```bash
node build.mjs                 # Node 18+, no dependencies
```

It writes `dist/index.html` (English, the default), `dist/<lang>/index.html` for
every other language, plus the assets, `sitemap.xml`, `.nojekyll` and `CNAME`.
The build exits non-zero if a `{{placeholder}}` has no matching key, so a
half-translated file fails the CI rather than shipping a page with holes in it.

## Change a string

Edit `content/en.json` and `content/fr.json`. Both must carry the same keys.
Values may contain inline `<code>` and `<strong>` — they are injected as HTML,
so keep them free of anything you would not write by hand.

## Add a language

Drop a `content/<lang>.json` next to the others, translated from `en.json`, and
give it a `label` — that string is what the language menu shows. Nothing else to
declare: the build discovers content files, generates the page at `dist/<lang>/`,
adds it to the language menu, to `sitemap.xml` and to the `hreflang` set.
English stays the default and keeps the root URL.

## Screenshots

The captures are generated, not taken by hand:

```bash
bash tools/shots.sh      # from the repository root: site/tools/shots.sh
node build.mjs           # pick the new files up
```

It starts a throwaway FaaSBox, fills it with demo content and photographs the
editor in both themes. See `tools/README.md` for what it touches and how to add
a capture. Your own database is never opened.

To add one by hand instead, drop the file in `assets/shots/` under the name the
placeholder shows. That is the whole procedure: the build looks at what is on
disk, renders an `<img>` for every capture it finds and a placeholder for the
rest, and reads each PNG's real dimensions out of its header so `width`/`height`
can never drift from the file.

**Two themes.** `name.png` is the light capture; `name-dark.png` is its dark
counterpart, picked up by convention with nothing to declare. When both exist
the page emits both and shows the one matching the active theme. Both are
`loading="lazy"`, so the hidden one is never fetched.

The `size` field in the content files is only the placeholder's fallback ratio,
used before the file exists. The `alt` text next to it is real content — keep it
translated and descriptive.

Captures are 2× the largest box they can occupy, saved as indexed PNG
(`convert in.png -colors 256 -strip PNG8:out.png`), which cuts UI screenshots to
roughly a third of their truecolor size with no visible loss.

## Publishing

The site is served at **https://faasbox.net/**.

Set **Settings → Pages → Source** to **GitHub Actions**. The workflow uploads
`dist/` as the Pages artifact; the build writes a `CNAME` file into it, so the
custom domain survives every deployment instead of relying on the repository
setting alone.

The domain lives in one place — the `SITE` constant at the top of `build.mjs`.
It feeds the canonical URLs, the `hreflang` set, `sitemap.xml` and `CNAME`.
Change it there and nothing else needs touching.

### DNS

At the registrar, for the apex `faasbox.net`:

| Type | Name | Value |
|---|---|---|
| A | `@` | `185.199.108.153` |
| A | `@` | `185.199.109.153` |
| A | `@` | `185.199.110.153` |
| A | `@` | `185.199.111.153` |
| AAAA | `@` | `2606:50c0:8000::153` |
| AAAA | `@` | `2606:50c0:8001::153` |
| AAAA | `@` | `2606:50c0:8002::153` |
| AAAA | `@` | `2606:50c0:8003::153` |
| CNAME | `www` | `antoineviau.github.io.` |

GitHub shows the current addresses under **Settings → Pages** once the domain is
entered — trust that screen over this table if the two ever disagree. With the
apex set as the custom domain, `www.faasbox.net` redirects to it automatically.

Enable **Enforce HTTPS** once the certificate is issued; it takes up to an hour
after the DNS propagates.

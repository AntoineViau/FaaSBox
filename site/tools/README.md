# Screenshot tooling

Regenerates `assets/shots/` from a real, running FaaSBox. Run it whenever the
editor changes shape and the captures on the site stop matching the product.

```bash
bash site/tools/shots.sh          # from the repository root
cd site && node build.mjs         # pick the new files up
```

Needs Go, Bun, Node 23+, `google-chrome` and ImageMagick's `convert`.

## What it does

`shots.sh` builds the UI and the server, starts an instance on a **throwaway
database** in a temp directory, and tears it all down afterwards. Your own
`data/pb_data` and `server/functions` are never opened, and the instance listens
on `127.0.0.1:8099` (override with `PORT=…`).

`seed.mjs` fills it with the demo content the screenshots show: six functions
with their code, secrets and schedules, plus log rows. One of them,
`summarise-events`, is deliberately the only function that completes **without
network access** — it is the one the capture opens, so the runner shows a real
success rather than a connection error.

`capture.mjs` drives Chrome over the DevTools Protocol: it plants the superuser
token in `localStorage` so the app never shows `/login`, opens the function,
types the payload, runs it, and photographs the editor at 2× — once in the light
theme, once in the dark one. Each capture is then downscaled and indexed to 256
colours, which costs nothing visible on UI screenshots and cuts the file to
roughly a third.

## Adding a capture

Add an entry to `TARGETS` in `capture.mjs`:

```js
const TARGETS = [
  { name: 'editor-full', selector: 'div.mx-auto.flex.h-screen' },
  { name: 'triggers-tab', selector: 'app-cron-editor' },
];
```

The name becomes the file name, and `-dark` is appended for the dark pass. Then
reference `assets/shots/<name>.png` from a content file — the site build finds
the dark variant by itself.

Selectors are read straight off the Angular templates in `ui/src/app/editor/`.
Two traps worth knowing: function rows in the sidebar are clickable `div`s, not
`button`s (the buttons are the per-row delete icons), and the runner's payload
field is a CodeMirror instance, so its content has to be typed through key
events rather than assigned.

## Known rough edges

**The server runs from its temp directory, deliberately.** The record hooks that
mirror a saved function to disk are built with `syncFunctionRecordHook(ctx,
functionsDir)`, which reads `functionsDir` when the hook is *registered* —
before Cobra has parsed `--functionsDir`. They therefore always write to
`./functions`, relative to the working directory, while `/invoke`, the boot-time
sync and the delete hook all read the flag from a closure and see the real
value. With the default flag the two coincide, which is why nothing shows in
normal use; pass `--functionsDir` and saved functions land somewhere the
invoker never looks. `shots.sh` starts the server from `$WORK` so the two agree.

**Every log row reads "just now".** `created` is a PocketBase autodate the
server overwrites on insert, so the seeded rows all land at the same instant.
Spreading them would mean writing to the SQLite file directly.

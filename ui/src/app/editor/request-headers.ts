/**
 * The headers the Runner sends, as a table and three pure functions — the model
 * of `cron-presets.ts`: no component, no state.
 *
 * Two callers need the same rules and neither owns them. The editor of the rows
 * decides what to warn about, the panel that fires the request decides what to
 * put on the wire, and a rule written in only one of the two is a rule the
 * other will end up contradicting.
 */

/** One row of the header editor. The order is the user's, and is kept. */
export type HeaderRow = { name: string; value: string };

/**
 * What the editor starts on.
 *
 * A body sent as a string makes `HttpClient` announce `text/plain;charset=UTF-8`,
 * and that — not the JSON everyone assumes — is what the function would read in
 * `headers["content-type"]`. The common case is therefore spelled out rather
 * than inherited. The row deletes like any other: a body that is not JSON says
 * so by removing it.
 *
 * The second row is there for the template a new function is created with,
 * which reads it: an editor that showed how to read a header and sent none
 * would be demonstrating an empty value. Both the name and the value say what
 * they are, since neither stands for anything in particular, and the row
 * deletes like any other.
 *
 * A function, not a constant: the caller edits what it gets back, and a shared
 * array would carry one panel's edits into the next.
 */
export function defaultHeaders(): HeaderRow[] {
  return [
    { name: 'Content-Type', value: 'application/json' },
    { name: 'X-Header-Name', value: 'header value' },
  ];
}

/**
 * The header names the server drops from the envelope, mirrored from
 * `deniedHeaders`. They carry what authenticates the caller, and a caller's
 * credentials are never handed to the code being called.
 *
 * This copy is a **mirror, not the rule**: the rule is enforced server-side and
 * holds whatever this file says. Falling behind an addition over there costs a
 * warning not shown here — never a header that gets through.
 */
const DENIED = ['x-api-key', 'authorization', 'cookie', 'proxy-authorization'];

/** A header name as HTTP defines it: the token of RFC 9110 §5.6.2. */
const TOKEN = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/;

/**
 * What is worth saying about a name that was typed, or `''` when there is
 * nothing to say.
 *
 * An empty name says nothing: it is the row someone just added and has not
 * filled yet, and greeting it with a complaint would be scolding a click.
 * `authorization` gets its own wording because it fails twice over — the editor
 * session token replaces it before the request leaves, so the value typed is
 * not even the one that travels.
 */
export function headerNotice(name: string): string {
  const trimmed = name.trim();
  if (!trimmed) return '';
  if (!TOKEN.test(trimmed)) return 'Not a usable header name — this row is not sent.';

  const lowered = trimmed.toLowerCase();
  if (lowered === 'authorization') {
    return 'Replaced by the editor session token, and never reaches the function.';
  }
  return DENIED.includes(lowered)
    ? 'Never reaches the function: what authenticates the caller is not forwarded.'
    : '';
}

/**
 * The rows as `HttpClient` takes them.
 *
 * A nameless row is dropped rather than refused — it is an empty row, not a
 * mistake — and so is a name HTTP would not accept, which the browser throws on
 * rather than reports. Both are what `headerNotice` has already said on screen.
 *
 * A name given twice keeps the last value: the object has one slot per name,
 * and merging the two would be inventing a rule nobody asked for.
 */
export function headerRecord(rows: HeaderRow[]): Record<string, string> {
  const record: Record<string, string> = {};
  for (const row of rows) {
    const name = row.name.trim();
    if (!name || !TOKEN.test(name)) continue;
    record[name] = row.value;
  }
  return record;
}

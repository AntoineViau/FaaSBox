/**
 * The request the Runner sends, as pure functions — the model of
 * `cron-presets.ts`: no component, no state.
 *
 * Two callers need the same rules and neither owns them. The editor of the rows
 * decides what to warn about, the panel that fires the request decides what to
 * put on the wire, and a rule written in only one of the two is a rule the
 * other will end up contradicting.
 *
 * The starting body sits here beside the starting headers although the file is
 * named for the latter, and deliberately: those two and the template a new
 * function is created with form one thing, and moving a name on one side
 * without the other breaks the example without breaking anything visible.
 *
 * The storage format lives here too. The sample is a field of the function, so
 * the rows have to become text and come back — and both halves of that
 * round trip have to agree.
 */

/** One row of the header editor. The order is the user's, and is kept. */
export type HeaderRow = { name: string; value: string };

/**
 * The headers a function whose sample was never customised starts on.
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
 * The body a function whose sample was never customised starts on.
 *
 * It is the field the template of a new function reads, so a first click on Run
 * answers something rather than `undefined`. It moves with that template and
 * with `defaultHeaders` above, never on its own.
 */
export function defaultBody(): string {
  return '{\n  "name": "world"\n}';
}

/**
 * The body a stored sample shows. An empty column is not an empty body: it
 * means nobody ever customised this function's sample, which is what covers
 * every function written before the field existed and every one an agent
 * writes through the management API.
 */
export function readSampleBody(stored: string): string {
  return stored || defaultBody();
}

/**
 * The rows a stored sample shows, and the counterpart of `serializeHeaders`.
 *
 * Anything that will not read back as rows falls to `defaultHeaders()`: the
 * column is editable from the PocketBase admin, so what comes out of it is
 * whatever someone typed, and a panel that threw on it would be a panel one
 * bad character can take off the screen. An entry that is not a well-formed row
 * is dropped rather than repaired — the shape is trivial, and guessing at half
 * of one would put a header on the wire nobody wrote.
 *
 * An empty array is left alone, and is not the same thing as an empty column: a
 * function whose sample sends no header at all is a legitimate thing to save.
 */
export function readSampleHeaders(stored: string): HeaderRow[] {
  if (!stored) return defaultHeaders();
  let parsed: unknown;
  try {
    parsed = JSON.parse(stored);
  } catch {
    return defaultHeaders();
  }
  if (!Array.isArray(parsed)) return defaultHeaders();
  return parsed
    .filter(
      (row): row is HeaderRow =>
        !!row && typeof row === 'object' &&
        typeof (row as HeaderRow).name === 'string' &&
        typeof (row as HeaderRow).value === 'string',
    )
    .map((row) => ({ name: row.name, value: row.value }));
}

/**
 * The rows as the record stores them.
 *
 * A plain JSON array, in a text column: what encrypts at rest are the text
 * columns, so a JSON field would keep its shape and travel to the replica in
 * the clear. The order is the user's and the format keeps it.
 *
 * It is also what tells the editor the panel was touched: two samples are the
 * same when they serialise the same.
 */
export function serializeHeaders(rows: HeaderRow[]): string {
  return JSON.stringify(rows.map((row) => ({ name: row.name, value: row.value })));
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

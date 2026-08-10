/**
 * The five tabs of the editor, and the slug each one answers to in the URL.
 *
 * The slug is deliberately not the label: `package-json`, not `package.json`.
 * A dot inside a URL segment works server-side — the static handler decides on
 * a stat, not on an extension — but tying the address to the displayed label
 * would break every saved link the first time a tab is renamed.
 */
export const EDITOR_TABS = [
  { slug: 'script', label: 'Script' },
  { slug: 'package-json', label: 'package.json' },
  { slug: 'triggers', label: 'Triggers' },
  { slug: 'environment', label: 'Environment' },
  { slug: 'files', label: 'Files' },
] as const;

export type EditorTabSlug = (typeof EDITOR_TABS)[number]['slug'];

export const EDITOR_TAB_SLUGS: readonly EditorTabSlug[] = EDITOR_TABS.map((t) => t.slug);

export const DEFAULT_EDITOR_TAB: EditorTabSlug = 'script';

/** What the URL carries is a string like any other: it may name no tab at all. */
export function isEditorTabSlug(value: string | null | undefined): value is EditorTabSlug {
  return !!value && (EDITOR_TAB_SLUGS as readonly string[]).includes(value);
}

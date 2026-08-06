import { completeFromList, ifNotIn, snippetCompletion } from '@codemirror/autocomplete';
import { javascriptLanguage } from '@codemirror/lang-javascript';

/**
 * Node types where nothing should be proposed: inside a string, a comment, a
 * declaration position, or right after a member access.
 *
 * Copied from `dontComplete` in @codemirror/lang-javascript, which does not
 * export it. An upstream change would go unnoticed here, with no worse effect
 * than a proposal in a place that does not call for one.
 */
const dontComplete = [
  'TemplateString',
  'String',
  'RegExp',
  'LineComment',
  'BlockComment',
  'VariableDefinition',
  'TypeDefinition',
  'Label',
  'PropertyDefinition',
  'PropertyName',
  'PrivatePropertyDefinition',
  'PrivatePropertyName',
  'JSXText',
  'JSXAttributeValue',
  'JSXOpenTag',
  'JSXCloseTag',
  'JSXSelfClosingTag',
  '.',
  '?.',
];

/**
 * The FaaSBox function contract, as completion entries.
 *
 * They restate what `docs/03-writing-functions.md` documents — read the JSON
 * payload from stdin, write a JSON result to stdout, log to stderr, read a
 * secret. They are not a second dialect: if one of the two moves, both move.
 *
 * The `faasbox-` prefix is the mechanism, not a decoration. Nothing surfaces
 * until it has been typed, so these entries never mix with the identifiers of
 * the file nor with the language snippets — which is what makes it safe to add
 * more of them later.
 */
const snippets = [
  snippetCompletion(
    'const payload = await Bun.stdin.text();\nconst body = JSON.parse(payload || "{}");\n\n${}\n\nconsole.log(JSON.stringify({ ok: true }));',
    { label: 'faasbox-handler', detail: 'function skeleton', type: 'text' },
  ),
  snippetCompletion('const body = JSON.parse((await Bun.stdin.text()) || "{}");', {
    label: 'faasbox-input',
    detail: 'read the JSON payload',
    type: 'text',
  }),
  snippetCompletion('console.log(JSON.stringify(${result}));', {
    label: 'faasbox-output',
    detail: 'return a JSON result',
    type: 'text',
  }),
  snippetCompletion('console.error(${message});', {
    label: 'faasbox-log',
    detail: 'write to stderr and the logs',
    type: 'text',
  }),
  snippetCompletion('const ${name} = process.env.${KEY};', {
    label: 'faasbox-env',
    detail: 'read a secret',
    type: 'text',
  }),
];

/**
 * Registered on `javascriptLanguage`, not on `typescriptLanguage`: the latter
 * is defined as `javascriptLanguage.configure({ dialect: 'ts' })` and shares
 * its language data, so the TypeScript mode picks these up too. `javascript()`
 * publishes its own completions through that very channel.
 */
export const faasboxCompletions = javascriptLanguage.data.of({
  autocomplete: ifNotIn(dontComplete, completeFromList(snippets)),
});

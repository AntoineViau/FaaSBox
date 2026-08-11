/**
 * The integration snippet of each client, in both shapes.
 *
 * The page composes nothing: it shows the snippet with the address of *this*
 * instance already in it, and the user copies. Written by hand, that address is
 * the one thing everyone gets wrong — and an example carved on localhost works
 * nowhere else.
 *
 * Two shapes, because there are two ways to prove who is calling. With OAuth
 * available the authentication header simply disappears: the client discovers
 * where to authenticate from the challenge `/mcp` answers, opens a browser, and
 * no secret is ever pasted anywhere. Without it — no `FAASBOX_PUBLIC_URL` on the
 * instance — the key form is the only one that works, and it stays on offer
 * either way for a non-interactive integration.
 *
 * The key is a placeholder, for the reason the invoke banner gives: a key's
 * value is only ever shown at creation, so the editor has nothing real to put
 * there.
 */
export type AgentClient = {
  readonly id: string;
  /** What the user calls it. */
  readonly name: string;
  /** Where the snippet goes: a terminal, or a file that has to be named. */
  readonly target: string;
  readonly snippet: string;
};

/** The placeholder every key-carrying snippet uses, spelled once. */
export const KEY_PLACEHOLDER = 'fbx_your_key_here';

/** The header name the key travels in, spelled once. */
const KEY_HEADER = 'X-API-Key';

/**
 * The four snippets for an endpoint, with or without the key header.
 *
 * Four clients and four shapes, because their mechanisms differ: a terminal
 * command for Claude Code, a TOML block for Codex, a JSON block for OpenCode,
 * and the generic `mcpServers` shape most of the others read.
 */
export function agentSnippets(url: string, withKey: boolean): AgentClient[] {
  const headers = withKey ? { [KEY_HEADER]: KEY_PLACEHOLDER } : undefined;

  return [
    {
      id: 'claude-code',
      name: 'Claude Code',
      target: 'run it in a terminal',
      snippet: withKey
        ? `claude mcp add --transport http faasbox ${url} \\\n  --header "${KEY_HEADER}: ${KEY_PLACEHOLDER}"`
        : `claude mcp add --transport http faasbox ${url}`,
    },
    {
      id: 'codex',
      name: 'Codex',
      target: '~/.codex/config.toml',
      snippet: withKey
        ? `[mcp_servers.faasbox]\nurl = "${url}"\nhttp_headers = { "${KEY_HEADER}" = "${KEY_PLACEHOLDER}" }`
        : `[mcp_servers.faasbox]\nurl = "${url}"`,
    },
    {
      id: 'opencode',
      name: 'OpenCode',
      target: 'opencode.json',
      snippet: JSON.stringify(
        { mcp: { faasbox: { type: 'remote', url, enabled: true, ...(headers && { headers }) } } },
        null,
        2,
      ),
    },
    {
      id: 'generic',
      name: 'Any other client',
      target: 'the mcpServers block most of them read',
      snippet: JSON.stringify(
        { mcpServers: { faasbox: { type: 'http', url, ...(headers && { headers }) } } },
        null,
        2,
      ),
    },
  ];
}

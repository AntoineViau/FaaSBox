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
 *
 * A third way needs no client configuration at all: a prompt pasted into the
 * conversation, sending the agent to the guide the server publishes at
 * `/llms.txt` (`llmsPrompt` below).
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
 * Where this instance publishes the guide an agent reads when it has no MCP
 * client — the writing contract and the HTTP API, served by `llms.go`.
 */
export function llmsGuideUrl(origin: string): string {
  return `${origin}/llms.txt`;
}

/**
 * The prompt that plugs in an agent without any configuration: read the guide,
 * then send the key on every request.
 *
 * It carries the absolute address because the guide does not: the server
 * writes no origin read off a request, so its paths are relative to wherever
 * the agent fetched it — and this prompt is where the agent learns that.
 *
 * One entry in the shape of the others, so the page shows it with the same
 * card and the same copy button.
 */
export function llmsPrompt(origin: string): AgentClient {
  return {
    id: 'llms-prompt',
    name: 'Any agent that can send HTTP requests',
    target: 'paste it into the conversation',
    snippet:
      `Read ${llmsGuideUrl(origin)} and follow it to work with my FaaSBox instance at ${origin}.\n` +
      `Send this header on every request: ${KEY_HEADER}: ${KEY_PLACEHOLDER}`,
  };
}

/**
 * What the client calls this server, given the name of the instance.
 *
 * The rule is the one the server applies to the identity it announces
 * (`mcpIdentity`, mcp.go): unnamed is `faasbox`, named is `faasbox-<name>`. It
 * is spelled here a second time because the page has to show the key a user
 * would write, and the instance route hands it the name and nothing else.
 *
 * An unnamed instance therefore keeps the snippets it has always had, to the
 * byte — which matters, since they are copied into files that already exist.
 */
function serverKey(instanceName: string): string {
  return instanceName ? `faasbox-${instanceName}` : 'faasbox';
}

/**
 * The same key, safe to write as a TOML table name.
 *
 * A name may carry a dot, and `[mcp_servers.faasbox-my.box]` is not a table
 * called `faasbox-my.box`: TOML reads it as `mcp_servers` → `faasbox-my` →
 * `box`, and the configuration is silently wrong. Quoting makes it one key
 * again. Quoted only when it has to be, so the unnamed snippet is unchanged.
 */
function tomlKey(key: string): string {
  return key.includes('.') ? `"${key}"` : key;
}

/**
 * The four snippets for an endpoint, with or without the key header.
 *
 * Four clients and four shapes, because their mechanisms differ: a terminal
 * command for Claude Code, a TOML block for Codex, a JSON block for OpenCode,
 * and the generic `mcpServers` shape most of the others read.
 *
 * The name of the instance is a parameter and not a default, so no call site
 * can quietly drop it and offer a snippet that collides with another box.
 */
export function agentSnippets(url: string, withKey: boolean, instanceName: string): AgentClient[] {
  const headers = withKey ? { [KEY_HEADER]: KEY_PLACEHOLDER } : undefined;
  const key = serverKey(instanceName);
  const toml = tomlKey(key);

  return [
    {
      id: 'claude-code',
      name: 'Claude Code',
      target: 'run it in a terminal',
      snippet: withKey
        ? `claude mcp add --transport http ${key} ${url} \\\n  --header "${KEY_HEADER}: ${KEY_PLACEHOLDER}"`
        : `claude mcp add --transport http ${key} ${url}`,
    },
    {
      id: 'codex',
      name: 'Codex',
      target: '~/.codex/config.toml',
      snippet: withKey
        ? `[mcp_servers.${toml}]\nurl = "${url}"\nhttp_headers = { "${KEY_HEADER}" = "${KEY_PLACEHOLDER}" }`
        : `[mcp_servers.${toml}]\nurl = "${url}"`,
    },
    {
      id: 'opencode',
      name: 'OpenCode',
      target: 'opencode.json',
      snippet: JSON.stringify(
        { mcp: { [key]: { type: 'remote', url, enabled: true, ...(headers && { headers }) } } },
        null,
        2,
      ),
    },
    {
      id: 'generic',
      name: 'Any other client',
      target: 'the mcpServers block most of them read',
      snippet: JSON.stringify(
        { mcpServers: { [key]: { type: 'http', url, ...(headers && { headers }) } } },
        null,
        2,
      ),
    },
  ];
}

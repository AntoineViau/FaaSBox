# 13 - AI Agents

FaaSBox speaks [MCP](https://modelcontextprotocol.io) — the Model Context Protocol — at `POST /mcp`. An agent connected to that endpoint can list, read, write, run and inspect the functions of your instance, and is handed the contract for writing one the moment it connects.

That last part is the point. The [management API](09-api-reference.md#3-manage-functions) already lets a program write a function with an API key, but it says nothing about *how* a FaaSBox function is written: an agent pointed at it has to be taught the contract by you, at every session, and finds the limits by hitting them. The MCP server declares its tools with their schemas and its instructions with the contract, so "here is a URL and a key, work it out" becomes "write me a function that emails me every morning".

## What You Need

**An API key with `canManage`, and with an open scope.** Both halves matter:

- Without `canManage`, the endpoint answers `403`. Invoking a function and rewriting one are two different rights (see [06 - API Keys & Security](06-api-keys-and-security.md)).
- With a **restricted** scope, the agent can read, change, run and delete the functions the scope names — but it **cannot create any**, because there is nothing to compare a scope against for a function that does not exist yet.

> **Read this before you create the key.** An agent that can create functions therefore holds a key with no scope restriction, which reaches **every function of this instance** — reading them, replacing them, running them, deleting them and their history. And a function is arbitrary code that FaaSBox executes with that function's secrets in its environment and your instance's network within reach. This is the real trade-off of plugging an agent in, well ahead of any question of transport. There is no narrower key that creates today.
>
> Two things follow. Set an **expiry** on that key — the creation form proposes 30 days as soon as you tick the box. And treat losing it as you would treat losing a shell on the machine: revoke it from the [API keys](06-api-keys-and-security.md) page and issue another.

Create the key from the **API keys** page of the editor: tick **Can manage functions**, leave the scope on **All functions**, and copy the value — it is shown once and never again.

## Connecting

The editor has an **AI agents** page, in the sidebar under **API keys**. It carries the snippet for each client with the address of *this* instance already in it: copy it, replace `fbx_your_key_here` with your key, and you are done. Nothing there needs composing by hand.

The snippets it shows:

**Claude Code** — run it in a terminal:

```bash
claude mcp add --transport http faasbox https://your-instance/mcp \
  --header "X-API-Key: fbx_your_key_here"
```

**Codex** — in `~/.codex/config.toml`:

```toml
[mcp_servers.faasbox]
url = "https://your-instance/mcp"
http_headers = { "X-API-Key" = "fbx_your_key_here" }
```

**OpenCode** — in `opencode.json`:

```json
{
  "mcp": {
    "faasbox": {
      "type": "remote",
      "url": "https://your-instance/mcp",
      "enabled": true,
      "headers": { "X-API-Key": "fbx_your_key_here" }
    }
  }
}
```

**Anything else** — most clients read this shape:

```json
{
  "mcpServers": {
    "faasbox": {
      "type": "http",
      "url": "https://your-instance/mcp",
      "headers": { "X-API-Key": "fbx_your_key_here" }
    }
  }
}
```

The transport is **Streamable HTTP**. There is no stdio variant: it would mean publishing an artifact and asking you to run a second runtime next to the one you already deployed.

## What the Agent Gets

Seven tools, one per verb the API already publishes:

| Tool | What it does |
|---|---|
| `list_functions` | Lists the functions the key may reach, with their id and invocation path. |
| `get_function` | Reads one: script, `package.json`, triggers, install state. |
| `create_function` | Creates one, with its triggers and its secrets. |
| `update_function` | Updates one in place, merging onto what is stored. |
| `delete_function` | Deletes one, its triggers and its history. |
| `invoke_function` | Runs one now and returns the result, `stderr` and the duration. |
| `get_function_logs` | Reads the last runs — the only way to see what a cron trigger did. |

Every tool goes through the same code path as its HTTP route, so what an agent may do is exactly what the key may do, refusals included.

On top of that, the session receives **instructions** at connect time: the execution contract (payload on `stdin`, JSON result on `stdout`, diagnostics on `stderr`), the naming rule, the size caps, the fact that dependencies install in the background, the cron expression format, and what a write replaces. You do not have to paste any of it into your agent.

## Three Things the Agent Is Told, and You Should Know Too

These are the traps of the underlying contract. The tool descriptions state them, which is what keeps an agent from walking into them — but they are worth knowing when you read what it did.

1. **`update_function` merges, it does not replace.** The underlying `PUT` replaces the whole function: a body carrying only `script` clears `packageJson`, and the dependencies with it. The tool reads the function first and re-sends what you did not mention, so an agent fixing three lines does not destroy your dependencies. Sending an **empty string** for `packageJson` still clears it — that is an explicit instruction, not an omission.
2. **`plainEnv` sent as `{}` deletes every secret** of the function, with no confirmation and no way back. Leaving it out preserves them, and that is what the tool does unless the agent explicitly sends secrets.
3. **`delete_function` destroys the execution history** along with the function and its triggers. There is no undo. Its description says so; ask before letting an agent "tidy up".

## What It Cannot Do

The MCP server is exactly as wide as a `canManage` key, no wider:

- **It cannot create API keys.** `POST /api/faasbox/keys` stays superuser-only, so an agent cannot forge itself a better key.
- **It cannot read your secrets back.** It writes environment variables, it never reads them — that endpoint stays superuser-only too.
- **It cannot reach the PocketBase admin, your collections, or anything outside `faasbox_functions` and the history of those functions.**

## Troubleshooting

| What you see | What it means |
|---|---|
| `401 Missing X-API-Key header` | The header did not reach the server. Check the quoting in your client's config. |
| `401 Invalid API key` | The value is not a key of this instance, or it was mistyped. |
| `403 API key is not authorized to manage functions` | The key is valid but lacks `canManage`. Tick the box on a new key; the flag cannot be added afterwards from the editor. |
| `403 API key has expired` | Exactly that. Create another. |
| `403 API key scope cannot be read` | The `allowedFunctions` field of the key was hand-edited into something that is not a list of ids. Fix it in the PocketBase admin, or create another key. |
| The agent connects but creating fails | The key has a restricted scope. A scoped key changes what it names and creates nothing. |

## What Comes Next

Authentication is by API key today, which means the key sits in your client's configuration file. An OAuth flow — where you click "authorise" in a browser and no key is ever pasted anywhere — is the intended installation path and is not here yet. When it lands, the header disappears from the snippets above and nothing else about the tools changes.

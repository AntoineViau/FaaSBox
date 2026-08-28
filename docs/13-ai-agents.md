# 13 - AI Agents

FaaSBox speaks [MCP](https://modelcontextprotocol.io) — the Model Context Protocol — at `POST /mcp`. An agent connected to that endpoint can list, read, write, run and inspect the functions of your instance, and is handed the contract for writing one the moment it connects.

That last part is the point. The [management API](09-api-reference.md#3-manage-functions) already lets a program write a function with an API key, but it says nothing about *how* a FaaSBox function is written: an agent pointed at it has to be taught the contract by you, at every session, and finds the limits by hitting them. The MCP server declares its tools with their schemas and its instructions with the contract, so "here is a URL and a key, work it out" becomes "write me a function that emails me every morning".

## Two Ways In, and They Grant the Same Thing

An agent proves who it is in one of two ways.

**By clicking Authorize.** You paste a URL into your client, run its authentication command, and a browser opens on this editor: it names the agent, says what authorizing hands over, and waits for your click. No secret is pasted anywhere, and you can cut the agent off later from the same page. This is the intended path.

**By pasting an API key.** The key carries `canManage` and travels in an `X-API-Key` header in your client's configuration file. This is the shape for a non-interactive integration — and the only one that works on an instance where `FAASBOX_PUBLIC_URL` is not set, since the browser flow needs the instance to know its own public address (see [10 - Deployment](10-deployment.md)).

> **Read this before you do either.** Both grant **everything**: reading, replacing, running and deleting **every function of this instance**, and reading their execution history. And a function is arbitrary code that FaaSBox executes, with that function's secrets in its environment and your instance's network within reach. This is the real trade-off of plugging an agent in, well ahead of any question of transport.
>
> There is no narrower grant today. A token carries no selector — the consent screen has nothing to tick, and says so. A key *can* be scoped, but a scoped key **creates nothing**: there is no way to compare a scope against a function that does not exist yet, so an agent that has to create functions holds a key that reaches all of them.
>
> Treat losing either as you would treat losing a shell on the machine. Revoke the agent from the **AI MCP** page, or the key from the [API keys](06-api-keys-and-security.md) page, and start over.

If you go the key route, create it from the **API keys** page of the editor: tick **Can manage functions**, leave the scope on **All functions**, set an expiry — the form proposes 30 days as soon as you tick the box — and copy the value. It is shown once and never again.

## Connecting

The editor has an **AI MCP** page, in the sidebar under **API keys**. It opens on the two ways in, each behind a closed panel — *Signin from your agent* and *Use an API key*. Open the one you mean to use: it explains that way, and carries the snippet for each client with the address of *this* instance already in it. Copy it, and you are done. Nothing there needs composing by hand.

On an instance with no `FAASBOX_PUBLIC_URL` the first panel is absent and the second is already open, since there is no choice left to make.

The transport is **Streamable HTTP**. There is no stdio variant: it would mean publishing an artifact and asking you to run a second runtime next to the one you already deployed.

### Signing in from the agent

Nothing to fill in but the address.

**Claude Code** — run it in a terminal, then `/mcp` in the session to authenticate:

```bash
claude mcp add --transport http faasbox https://your-instance/mcp
```

**Codex** — in `~/.codex/config.toml`:

```toml
[mcp_servers.faasbox]
url = "https://your-instance/mcp"
```

**OpenCode** — in `opencode.json`:

```json
{
  "mcp": {
    "faasbox": {
      "type": "remote",
      "url": "https://your-instance/mcp",
      "enabled": true
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
      "url": "https://your-instance/mcp"
    }
  }
}
```

What happens next is the client's business, not yours: it reads the address of the authorization server off the `401` FaaSBox answers, registers itself, and opens a browser on the consent screen. You read what it asks for and click **Authorize** — or **Deny**, and the agent reports a failure. See [09 - API Reference](09-api-reference.md#6-oauth-authorization) for the endpoints behind it.

### Pasting a key

Replace `fbx_your_key_here` with the key you created.

**Claude Code**:

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

**Anything else**:

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

A key and a token presented together is not an error: the key is what the request is weighed on, and the token is ignored.

## Cutting an Agent Off

The **AI MCP** page lists every agent that authorized itself: the name it registered under, the day you approved it, and the day its access expires on its own. **Revoke** ends one.

It takes effect on the agent's **next call** — there is no session to wait out — and it is final: the agent has to go through the consent screen again. An authorization also dies on its own after ninety days without use, and a token it renews with is refused for good if it is ever replayed, which is how a stolen configuration file announces itself.

Revoking an agent does not touch your API keys, and revoking a key does not touch your agents. They are separate lists on separate pages.

## What the Agent Gets

Seven tools, one per verb the API already publishes:

| Tool | What it does |
|---|---|
| `list_functions` | Lists the functions the key may reach, with their id and invocation path. |
| `get_function` | Reads one: script, `package.json`, sample call, triggers, install state. |
| `create_function` | Creates one, with its sample call, its triggers and its secrets. |
| `update_function` | Updates one in place, merging onto what is stored. |
| `delete_function` | Deletes one, its triggers and its history. |
| `invoke_function` | Runs one now and returns the result, `stderr` and the duration. |
| `get_function_logs` | Reads the last runs — the only way to see what a cron or startup trigger did. |

Every tool goes through the same code path as its HTTP route, so what an agent may do is exactly what the key may do, refusals included.

On top of that, the session receives **instructions** at connect time: the execution contract (the [call envelope](03-writing-functions.md#the-input-envelope) on `stdin`, JSON result on `stdout`, diagnostics on `stderr`), the naming rule, the size caps, the fact that dependencies install in the background, the two kinds of trigger — a cron expression, or a startup delay — and what a write replaces. You do not have to paste any of it into your agent.

## The Agent Can Write the Call, Not Just the Function

An agent that has just written a function knows the call it expects: it wrote the line reading `req.headers["stripe-signature"]`. It can save that call with the function — the body and the headers of the [sample the Editor's Runner replays](03-writing-functions.md#the-body-and-the-headers) — so the next person to open it can click **Run** instead of reconstructing the request by reading the script. It is documentation that runs, and its natural author is whoever wrote the code.

One rule comes with it, and the tool descriptions state it so the agent reads it too: **no real secret in a sample.** Everyone who can read the function reads its sample, and on a public demo instance that is everyone. What belongs there is the *shape* of the call — a signature header with a plausible value, a payload with the right fields. A real token goes in `plainEnv`, which is written and never read back.

## Four Things the Agent Is Told, and You Should Know Too

These are the traps of the underlying contract. The tool descriptions state them, which is what keeps an agent from walking into them — but they are worth knowing when you read what it did.

1. **`update_function` merges, it does not replace.** The underlying `PUT` replaces the whole function: a body carrying only `script` clears `packageJson`, and the dependencies with it. The tool reads the function first and re-sends what you did not mention, so an agent fixing three lines does not destroy your dependencies. Sending an **empty string** for `packageJson` still clears it — that is an explicit instruction, not an omission.
2. **`plainEnv` sent as `{}` deletes every secret** of the function, with no confirmation and no way back. Leaving it out preserves them, and that is what the tool does unless the agent explicitly sends secrets.
3. **`delete_function` destroys the execution history** along with the function and its triggers. There is no undo. Its description says so; ask before letting an agent "tidy up".
4. **A sample is read by everyone who can read the function.** It is encrypted at rest, which is not the same as private. Never a live secret.

## What It Cannot Do

The MCP server is exactly as wide as a `canManage` key with an open scope, no wider — and that is true of a token as much as of a key, since a token is worth exactly such a key:

- **It cannot create API keys.** `POST /api/faasbox/keys` stays superuser-only, so an agent cannot forge itself a better key.
- **It cannot read your secrets back.** It writes environment variables, it never reads them — that endpoint stays superuser-only too.
- **It cannot reach the PocketBase admin, your collections, or anything outside `faasbox_functions` and the history of those functions.**

## Troubleshooting

| What you see | What it means |
|---|---|
| `401 Missing X-API-Key header` | No credential reached the server at all. On an instance with the browser flow available, this same response tells your client where to go and authenticate — if it stops here, it does not speak OAuth, and needs a key. |
| `401 Invalid access token` | The token was refused: unknown, expired, issued for another server, or its authorization was revoked. Authenticate again from the agent. |
| `401 Invalid API key` | The value is not a key of this instance, or it was mistyped. |
| `403 API key is not authorized to manage functions` | The key is valid but lacks `canManage`. Tick the box on a new key; the flag cannot be added afterwards from the editor. |
| `403 API key has expired` | Exactly that. Create another. |
| `403 API key scope cannot be read` | The `allowedFunctions` field of the key was hand-edited into something that is not a list of ids. Fix it in the PocketBase admin, or create another key. |
| The agent connects but creating fails | The key has a restricted scope. A scoped key changes what it names and creates nothing. |
| The agent never opens a browser | The instance has no usable `FAASBOX_PUBLIC_URL` — unset, or set to something refused, a value with no `http://` or `https://` in front being the usual one — so it publishes no authorization server and its `401` carries no signpost. Use a key, or fix the variable; the startup log names the reason. |
| The agent worked yesterday and now gets `401` | Its authorization was revoked — from the **AI MCP** page, or by the server itself after a token was replayed. Authenticate again.  |

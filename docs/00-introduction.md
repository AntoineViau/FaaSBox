# 00 - Introduction

## What is FaaSBox?

**FaaSBox** is a "Functions-as-a-Service" platform designed for developers who want the simplicity of a backend-as-a-service (PocketBase) combined with the power of serverless-style function execution.

While PocketBase itself allows for Go hooks or JS VM hooks, FaaSBox takes a different approach by using **Bun** as an external runtime. This provides several advantages:
- **Full Node.js/Bun compatibility**: Use any npm package.
- **TypeScript by default**: No compilation step needed, Bun handles it natively.
- **Process Isolation**: Each function run is a separate subprocess, ensuring one crashing function doesn't take down the whole server.
- **Resource Control**: Easily monitor and limit stdout/stderr and execution time.

## Why use it?

FaaSBox is perfect for:
- **Webhooks**: Handling incoming data from Stripe, GitHub, etc.
- **Cron Jobs**: Running periodic cleanup tasks, data syncs, or reports.
- **API Glue**: Connecting different services together without setting up a full-blown backend.
- **Prototyping**: Rapidly deploying logic without worrying about infrastructure.

## Key Features

1.  **Hashed API Keys**: Secure authentication using industry-standard hashing.
2.  **Cron Scheduling**: Built-in scheduler for periodic tasks.
3.  **Encrypted Secrets**: Store sensitive API keys and tokens encrypted at rest (AES-256-GCM).
4.  **Integrated Editor**: A modern Angular-based web editor to write and test functions directly.
5.  **Execution Logs**: Detailed history of every function call, including duration, status, and output.
6.  **Automatic Dependencies**: Just add a `package.json`, and FaaSBox handles the `bun install` for you.
7.  **MCP Server**: An AI agent can write, run and inspect your functions over the Model Context Protocol, and is handed the writing contract when it connects.
8.  **OAuth for Agents**: FaaSBox is its own OAuth 2.1 authorization server, so an agent is authorized by a click in the browser rather than by a key you paste into its configuration.

## What FaaSBox Deliberately Does Not Do

The list below is not a roadmap. Each entry is a moving part taken out on
purpose, and knowing them is how you decide whether the product fits before you
build on it.

- **No multi-user.** A single superuser administers the instance. There is no
  permission matrix, no role, and no team. If several people need their own
  functions, they need their own instances.
- **No versioning and no rollback.** Saving replaces what was there. FaaSBox
  keeps no previous revision of a script and offers no way back to one — keep
  your functions in version control if that matters to you.
- **No response streaming.** An invocation returns a complete result or an
  error. There is nothing to reassemble on the caller's side, and no way to emit
  progress while a function runs.
- **No composition.** Functions do not call each other through the platform.
  Each one is written, invoked and tested on its own; a function that needs
  another one calls it over HTTP like any other client would.
- **No replay of missed schedules.** A cron trigger due while the server was down
  is reported, never fired late. See
  [05 - Scheduling (Cron)](05-scheduling-cron.md#when-the-server-is-down).

## Philosophy

FaaSBox follows the PocketBase philosophy: **Keep it simple, self-contained, and easy to deploy.** It's a single binary (or Docker image) that contains everything you need to run your logic in the cloud.

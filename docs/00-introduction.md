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

## Philosophy

FaaSBox follows the PocketBase philosophy: **Keep it simple, self-contained, and easy to deploy.** It's a single binary (or Docker image) that contains everything you need to run your logic in the cloud.

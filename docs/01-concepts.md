# 01 - Concepts

To effectively use FaaSBox, it's important to understand the core concepts and how they interact.

## The Execution Model

FaaSBox uses a **Stdin/Stdout** execution model. This is a classic Unix philosophy applied to serverless:

1.  **Trigger**: An HTTP request or a Cron job triggers a function.
2.  **Input**: The request body (JSON) is passed to the function's **stdin**.
3.  **Execution**: The Bun runtime executes the function (`index.ts`).
4.  **Output**: Anything the function writes to **stdout** is captured as the result.
5.  **Logs**: Anything written to **stderr** is captured as debug logs.

This model makes functions extremely easy to test locally: `echo '{"name": "Alice"}' | bun index.ts`.

## Functions as Folders

In FaaSBox, a "function" is simply a directory inside the `functions/` folder. 
- The **directory name** is the **function name**.
- The **entry point** is always `index.ts`.
- An optional `package.json` defines dependencies.

```text
functions/
  ├── my-logic/
  │   ├── index.ts
  │   └── package.json
  └── simple-echo/
      └── index.ts
```

## Security Model

Security is baked into the platform at multiple levels:

### 1. Authentication (API Keys)
Access to functions is protected by API keys. These keys are:
- **Opaque**: Randomly generated strings.
- **Hashed**: Only the SHA-256 hash is stored. Even if the database is leaked, the keys remain safe.
- **Scoped**: A key can be restricted to specific functions.
- **Expiring**: Keys can have an optional expiration date.

### 2. Environment Isolation
Functions do not have access to the host's environment variables. We explicitly whitelist what is passed to the Bun subprocess:
- `FUNCTION_NAME`
- `NODE_ENV=production`
- `PATH`, `HOME`
- Any **Secrets** you've explicitly configured for that function.

### 3. Secrets (Encrypted Env Vars)
Sensitive data (like database URLs or third-party API keys) are stored in the `faasbox_functions` collection. When you save a secret:
1.  You enter it in plain text in the UI.
2.  The server encrypts it using **AES-256-GCM**.
3.  The plain text is discarded.
4.  At execution time, the server decrypts the secret and injects it into the function's environment.

## Data Persistence

The **database is the single source of truth** for functions. All function code and metadata live in the `faasbox_functions` collection.

The file system is a derived cache used only for execution:
-  **DB to Disk (startup)**: At startup, all functions are restored from the database to the `functions/` directory on disk.
-  **DB to Disk (live)**: When you create, update, or delete a function via the UI, the changes are saved to the database and immediately synced to disk.

Functions are executed from disk by the Bun runtime, but the database is always the authoritative copy. This means a container restart simply recreates the files from the database.

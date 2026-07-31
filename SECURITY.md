# Security Policy

## Reporting a Vulnerability

**Do not open a public issue for a security vulnerability.** This repository is public: an issue describing a flaw, its reproduction steps and its impact publishes a working exploit before a fix exists.

Report it privately instead, through GitHub's private vulnerability reporting:

**[Report a vulnerability](https://github.com/AntoineViau/FaaSBox/security/advisories/new)** — or, from the repository page, the **Security** tab, then **Report a vulnerability**.

The report is visible only to you and the maintainer. It stays that way until an advisory is published, once a fix is available.

Include as much detail as possible:

- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

### Response time

FaaSBox is maintained by one person, on spare time. Expect an acknowledgement within **7 days** and an assessment within **30 days**. These are targets, not guarantees — if a report gets no answer within that window, a follow-up comment on the same advisory thread is welcome.

## Scope

FaaSBox is a **personal, single-user** platform. It is meant to be run by the same person who writes the functions it executes, and it is not designed to be exposed to untrusted users. That framing decides what counts as a vulnerability.

**In scope** — anything that lets a caller act without the instance owner's consent, or that leaks what the platform is supposed to keep:

- Bypassing API key authentication, or a key's scope, expiry, or revocation.
- Escaping the intended file layout — path traversal, reading or writing outside a function's directory.
- Disclosure of stored secrets: encrypted environment variables, API key material, or the encryption key itself, through a response, a log record, or the editor.
- Code execution reachable without a valid API key or a superuser session.
- Any bypass of the execution bounds (timeouts, body and output caps) that lets a request escape them entirely.

**Out of scope** — the platform doing what it was built to do:

- A function behaving maliciously. FaaSBox executes arbitrary TypeScript supplied by the instance owner, by design. Owner-authored code reading files, opening network connections, or consuming resources is the product working as intended, not a flaw.
- Anything that requires a superuser session, or a valid API key already scoped to the target function. Both already grant the capability being demonstrated.
- Resource exhaustion caused by the owner's own functions. Each invocation is bounded, but the owner is free to spend their own instance's capacity.
- The fact that a named function exists. `POST /invoke/{name}` answers `403` and names the function when it falls outside a key's scope, rather than hiding it behind a `404`. Only the *inventory* is withheld — `GET /functions` is filtered by scope. This is a documented, deliberate trade-off.
- Consequences of exposing an instance to untrusted users, which the platform does not claim to support.

If you are unsure which side a finding falls on, report it privately anyway. A misfiled report costs a reply; a publicly disclosed one cannot be taken back.

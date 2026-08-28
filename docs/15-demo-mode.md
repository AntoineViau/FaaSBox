# 15 - Demo Mode

`FAASBOX_DEMOMODE=true` turns an instance into a **showcase**: a FaaSBox anyone
can visit, look around, and change nothing in. It is what you deploy to show the
product, not to run anything with it.

## Turning it on

```bash
docker run -d -p 8080:8080 \
  -e SUPERUSER_EMAIL=demo@example.com \
  -e SUPERUSER_PASSWORD=a-long-random-password \
  -e FAASBOX_ENCRYPTION_KEY=$(openssl rand -hex 32) \
  -e FAASBOX_DEMOMODE=true \
  -e FAASBOX_DEMOMODE_EMAIL=demo@example.com \
  -e FAASBOX_DEMOMODE_PASSWORD=a-long-random-password \
  ghcr.io/antoineviau/faasbox:latest
```

Only the first of the three variables commands anything. A typo in it is caught
— `FAASBOX_DEMOMODE=treu` stops the server outright, naming the variable and the
value, rather than starting an instance that believes it is not a showcase. A
wrong decision is not caught: `false` on something you meant to publish is a
perfectly valid setting.

Turning the mode on takes a restart, since the three variables are read once at
startup. The switch is immediate for visitors all the same: the editor reads the
mode at every load, and the answer is never cached — someone who saw the
instance before the switch gets the showcase on their next visit, without having
to clear anything.

## What it shows, and what it stops

What it shows is everything — the functions and their code, their triggers,
their execution history, their folder on disk, the API keys, the authorized
agents. What it stops:

- **invocations**, by HTTP and from the editor's **Run** button alike;
- **cron triggers**: the scheduler is never armed, and nothing is filed as
  missed either;
- **startup triggers**: they are not armed either, so nothing fires when the
  instance comes up or is redeployed;
- the **dependency install** that normally runs at startup;
- the **hourly prune** of the execution logs — the history on display is
  precisely what has to stay intact.

An **AI agent cannot connect** to a demo instance, neither by API key nor by
OAuth: every MCP message is a `POST`. The **AI MCP** page still displays the
endpoint, the configuration snippets and the agents authorized before the
freeze — the showcase shows the feature, it does not serve it.

## How the read-only rule works

There is **no demo-specific authentication**, and that is the whole design. A
visitor signs in as an ordinary superuser, with an ordinary account, and gets an
ordinary session. Nothing about that account is special.

What makes the instance safe is a rule sitting beside authentication that never
asks who is connected: anything that is not a `GET`, a `HEAD` or an `OPTIONS` is
refused with a `403` and `{"error":"this instance is a read-only demo"}`.
Everyone is held to it equally — signed in or not, superuser or not. The rule is
the HTTP method rather than the route, which is what makes it hold: a route
added later is refused by default, without anyone having to remember it.

Deciding by method means two families of exception, and both are worth knowing
if you are reading server logs.

Three writes are allowed by name, because without them nobody could look at
anything: signing in, revalidating the session when the page loads, and placing
the editor's realtime subscriptions.

One read is refused: `GET /oauth/authorize`. It looks like a read and is not —
it records a pending authorization before redirecting — and since the hourly
prune is one of the things a showcase skips, a caller could otherwise loop it
and grow the table without bound. The rest of the OAuth flow is refused anyway,
so nothing is lost.

The PocketBase admin at `/_/` is covered by the same rule, since it is the same
server: a visitor with the published credentials gets in and reads, and nothing
more. Nothing there says "demo", though — that interface is not part of FaaSBox,
so it carries no banner and no prefilled field.

## The published credentials

`FAASBOX_DEMOMODE_EMAIL` and `FAASBOX_DEMOMODE_PASSWORD` **create no account**.
They fill the two fields of the sign-in form, and nothing else. The account they
name has to exist already and be a superuser — in practice the `SUPERUSER_EMAIL`
of the same instance, copied by hand. Leave them out and the mode still works;
the form is simply empty and the visitor has to be told what to type.

They are a separate pair rather than a reuse of `SUPERUSER_EMAIL` and
`SUPERUSER_PASSWORD` **because this route publishes them to the world**, and
publishing has to be a decision you take by name. Were the sign-in form filled
from the superuser variables, a single `FAASBOX_DEMOMODE=true` set by mistake on
a real instance would broadcast that instance's administrator password. With a
separate pair, the same mistake publishes nothing at all: the demo variables are
not set, so the form comes up empty. It is also the only thing that could work
in general — `SUPERUSER_PASSWORD` is optional, and an account created by hand in
the admin has none.

> ⛔ **Those credentials are published by a public, unauthenticated route.**
> `GET /api/faasbox/instance` hands them to anyone who asks — that is how the
> sign-in form fills itself in. So the account they name is open to the world,
> and so is everything the instance holds: code, secrets, history, files. Seed a
> demo instance with throwaway content and throwaway secrets, and never set this
> flag on an instance that carries anything you would not publish.

## Seeding an instance

Seed it before you freeze it: start it normally, write the functions, triggers
and secrets you want on display, run them a few times so the history has
something in it, then restart with the flag on.

That restart is the cost of the design. Because the rule never looks at who is
connected, there is no signed-in state that lets you edit a showcase in place —
changing what it shows means turning the flag off, changing it, and turning it
back on.

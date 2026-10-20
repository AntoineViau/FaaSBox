# 12 - Lost Password

If you've lost your superuser password, you reset it by starting the instance once with `SUPERUSER_RESET=true`, then starting it again without.

`SUPERUSER_EMAIL` and `SUPERUSER_PASSWORD` only **create** the account, on the first start of an instance that has none. Once it exists they are left alone: a password you change in the admin (`/_/`) survives every restart, and editing the variables afterwards changes nothing. `SUPERUSER_RESET` is the one thing that makes the server impose them again — and it is the only way back in on a host that lets you edit the environment without opening a shell.

## Docker

Recreate the container with the password you want, and the flag:

```bash
docker run -d -p 8080:8080 \
  -e SUPERUSER_EMAIL=admin@example.com \
  -e SUPERUSER_PASSWORD=yournewpassword \
  -e SUPERUSER_RESET=true \
  -e FAASBOX_ENCRYPTION_KEY=your_existing_64_char_hex_key \
  -v faasbox-data:/app/data/pb_data \
  ghcr.io/antoineviau/faasbox:latest
```

At startup the server finds the superuser by `SUPERUSER_EMAIL`, gives it `SUPERUSER_PASSWORD`, and prints a warning. Sign in, then recreate the container **without** `SUPERUSER_RESET`. Your data, functions, and API keys are preserved — only the password changes.

> ⚠️ **Remove the flag once you are signed in.** Left set, it imposes the variables again at every start: a password you change in the admin is undone by the next restart, the old one opens the account again, and every superuser session is signed out each time. The warning in the server log is printed at every start for as long as the flag is there.

> ⛔ **You must pass the same `FAASBOX_ENCRYPTION_KEY` you were already running with**, along with every other variable the instance had. This is not a precaution, it is the condition: the database is encrypted at rest, so **without that key the server will not start at all**, and nothing in it can be read again. A lost password is recoverable; a lost key is not. This page changes a password, not a configuration.
>
> If you no longer have the key, there is no way back into that database. Starting a fresh instance with a new key gives you a working server and an empty one.

## Docker Compose

Add `SUPERUSER_RESET=true` and the new password to your `docker-compose.yml` or `.env` file, then:

```bash
docker compose up -d --force-recreate
```

Sign in, remove the `SUPERUSER_RESET` line, and run the same command again.

On a host that only lets you edit the environment, the gesture is the same: add the variable, restart, sign in, remove it, restart.

## Local development

Pass the flag to the dev script, with the credentials you want:

```bash
SUPERUSER_RESET=true SUPERUSER_EMAIL=admin@example.com SUPERUSER_PASSWORD=yournewpassword bash infra/dev/dev.sh
```

Once signed in, stop it and start it again without `SUPERUSER_RESET`.

## Notes

- The flag finds the account **by address**. If `SUPERUSER_EMAIL` matches no superuser — because you changed the address in the admin, say — it creates a **second** superuser with that address and password, beside the renamed one. Sign in with it, then delete the account you no longer want in the admin, under `_superusers`.
- Without the flag, the server tells you when the variables no longer describe the account: a line at startup says that `SUPERUSER_PASSWORD` does not open it, or that no superuser has the address `SUPERUSER_EMAIL` names. The account is left as it stands. The line names the address, never the password.
- The flag needs both other variables. Set alone, or with only one of them, it touches no account, and a line at startup says so.
- A value of `SUPERUSER_RESET` that is not a boolean — `treu` — stops the server and names the variable, rather than guessing whether you meant to reset.
- No data is lost during this process. Only the superuser credentials are changed.

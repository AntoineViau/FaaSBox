# 14 - Litestream Replication

FaaSBox keeps everything in one SQLite file. That is what makes it small, and it
is also what makes it fragile in one specific place: a host whose filesystem
does not survive a redeploy hands you a fresh, empty disk every time you ship.

[Litestream](https://litestream.io/) is the answer the image ships with. It
mirrors that file continuously to any S3-compatible storage, and puts it back
before anything reads it on the next boot. Together with the fact that the
server rebuilds `functions/` from the database, that is what makes the container
disposable: the database comes back from S3, and everything else comes back from
the database.

**It is entirely optional**, and off unless you ask for it. Leave the variables
unset and FaaSBox starts normally, from the local file alone — which is the
right setup whenever your host gives you a volume that persists. See
[10 - Deployment](10-deployment.md#persistence) for that case, which is the one
this documentation recommends.

## Turning it on

| Variable | Description |
|----------|-------------|
| `LITESTREAM_REPLICA_ENDPOINT` | S3 endpoint (e.g. `s3.amazonaws.com`, `storage.googleapis.com`) |
| `LITESTREAM_REPLICA_BUCKET` | Bucket name — its presence is what turns replication on |
| `LITESTREAM_ACCESS_KEY_ID` | Access key |
| `LITESTREAM_SECRET_ACCESS_KEY` | Secret key |
| `LITESTREAM_REPLICA_PATH` | Prefix the replica is written under, `data.db` by default. Used exactly as given — no leading and no trailing slash |

```bash
docker run -d -p 8080:8080 \
  -e SUPERUSER_EMAIL=admin@example.com \
  -e SUPERUSER_PASSWORD=your_secure_password \
  -e FAASBOX_ENCRYPTION_KEY=$(openssl rand -hex 32) \
  -e LITESTREAM_REPLICA_ENDPOINT=s3.amazonaws.com \
  -e LITESTREAM_REPLICA_BUCKET=my-faasbox-backup \
  -e LITESTREAM_ACCESS_KEY_ID=your_access_key \
  -e LITESTREAM_SECRET_ACCESS_KEY=your_secret_key \
  -e LITESTREAM_REPLICA_PATH=faasbox/prod \
  ghcr.io/antoineviau/faasbox:latest
```

The bucket is the switch: name one and replication starts, leave it out and
nothing is mirrored.

## The prefix is the address of your data

`LITESTREAM_REPLICA_PATH` is what lets one bucket hold more than one instance,
or lets the replica sit under the prefix your storage policy asks for. Leave it
out and everything lands under `data.db/` at the root of the bucket, which is
what a bucket dedicated to one instance wants anyway.

It is the address of your data in both directions: the same value is what
`restore` reads on the next boot. What changing it costs you depends on whether
you kept a persistent volume.

With one — the setup [10 - Deployment](10-deployment.md#persistence) recommends
— nothing is lost. The local database is already there, so no restore is
attempted at all; the instance comes up on its own data and replication simply
starts writing under the new prefix. The old one is left untouched, holding
everything that was written under it.

Without a local database — a fresh container, a wiped volume — the restore looks
under the new prefix, finds nothing, and **starts from an empty database without
saying anything**. That silence is `-if-replica-exists` doing its job, since a
first boot has no replica to find either. The old prefix still holds your data,
so the fix is to put the value back and restart.

## What happens at boot

1. `litestream restore -if-db-not-exists -if-replica-exists` puts the database
   back before anything reads it — but only into an empty volume. **The local
   database wins**: if `pb_data/data.db` is already there, the restore is
   skipped and that file is what starts, untouched. The replica is a way to
   rebuild an instance that has lost its disk, never a source that overwrites a
   live one. A first boot with no replica passes silently too, so both a fresh
   deployment and every restart after it come up without an error. The one case
   this rule does not arbitrate for you is a local file that survived but is
   corrupt: it still wins, and the replica is not consulted. Falling back to it
   is a deliberate act — remove the volume, then restart.
2. The superuser is created or updated, if credentials were given.
3. Litestream starts the server as its **child** (`replicate -exec`) and mirrors
   every write as it happens. A clean shutdown of the server triggers a last
   sync.
4. The server rebuilds `functions/` from that database, then reinstalls the
   dependencies that the fresh filesystem does not have.

The configuration lives in `infra/production/litestream.yml`.

## One file is deliberately left out

Only `data.db` is replicated — your functions, triggers, secrets, keys and
execution history. PocketBase's own request log lives beside it in
`pb_data/auxiliary.db` and is not mirrored, so a restored instance comes up with
an empty `Logs` view in the admin UI. Nothing depends on it, but if you are
chasing an incident after a redeploy, the HTTP history of the container you
replaced is gone. See [07 - Execution Logs](07-execution-logs.md) for what that
view is and what it is not.

## The key is not in the bucket

The replica is encrypted content, not readable content: FaaSBox encrypts
function code, triggers, execution history and secrets at rest under
`FAASBOX_ENCRYPTION_KEY`, and that key never travels with the database.

Restoring onto a new host without it gives you a server that will not start —
which is the correct outcome, and a bad surprise on the day you need the
restore. Store the key where you store the credentials of the machine, not on
the machine, and keep it with the bucket credentials rather than with the
bucket. See [10 - Deployment](10-deployment.md#the-environment-variables) for
what the variable is and what happens without it, and
[06 - API Keys & Security](06-api-keys-and-security.md#what-is-encrypted-at-rest)
for the full inventory of what it covers.

## Backups are not the same guarantee

A Litestream mirror is always current: there is no window during which the last
hour is missing. A periodic dump is a different promise, and the two are worth
having together — see [Backup strategy](10-deployment.md#backup-strategy) for
the snapshot and the PocketBase archive that sit alongside this.

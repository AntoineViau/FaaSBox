# 12 - Lost Password

If you've lost your superuser password, you can reset it by restarting the container with new credentials.

## Docker

Update the `SUPERUSER_PASSWORD` environment variable and restart:

```bash
docker run -d -p 8080:8080 \
  -e SUPERUSER_EMAIL=admin@example.com \
  -e SUPERUSER_PASSWORD=yournewpassword \
  -v faasbox-data:/app/data/pb_data \
  faasbox
```

The entrypoint runs `faasbox superuser upsert` on every startup, so the password is updated automatically. Your data, functions, and API keys are preserved — only the password changes.

## Local development

Run the upsert command manually:

```bash
cd server
go run . superuser upsert admin@example.com yournewpassword --dir=../data/pb_data
```

## Docker Compose

Update the password in your `docker-compose.yml` or `.env` file, then:

```bash
docker compose up -d --force-recreate
```

## Notes

- The email must match the existing superuser account. If you also forgot the email, check your `SUPERUSER_EMAIL` environment variable or Docker Compose configuration.
- This works because FaaSBox uses PocketBase's `superuser upsert` command, which creates the account if it doesn't exist or updates it if it does.
- No data is lost during this process. Only the superuser credentials are changed.

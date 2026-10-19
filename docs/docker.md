# Docker installation (Linux)

For a public domain with HTTPS, use the [production deployment](production.md).
The setup below is for local access or an SSH tunnel.

Requires Docker Engine and the Docker Compose plugin. This setup builds a local
image from source; no prebuilt registry image is published yet.

```sh
git clone https://github.com/DaDecky/codex-transcribe.git
cd codex-transcribe
docker compose up -d --build
```

Open **http://127.0.0.1:8378/accounts** using that exact IP address:

1. Set your dashboard password (minimum 4 characters).
2. Add your ChatGPT account and complete browser sign-in. You can copy the login
   link to another browser and paste the full callback URL back into the dashboard.
3. Use **Test dictation** to record, play back, and transcribe a sample.
4. Create a consumer API key in the dashboard for your client. The client base
   URL is `http://127.0.0.1:8378/v1`; use the generated key as its Bearer token.

No `auth.json`, Codex installation, desktop keyring, or manually supplied session
secret is required. Your existing native-install accounts are not imported.

## Persistence and credentials

Compose stores accounts, refreshed tokens, dashboard password hash, generated
session secret, and consumer key hashes in the `dictate-data` named volume under
`/data/config/codex-dictate`. Account tokens are stored **unencrypted** in
`credentials.json`, with mode `0600` and a directory created with mode `0700`.
Protect the Docker host, volume, and any backups as credentials. This is an
explicit file-storage mode (`CODEX_DICTATE_CREDENTIAL_STORE=file`); native
installs keep using their desktop keyring by default, with no automatic fallback.

The container runs as UID/GID 10001, with a read-only root filesystem and a
temporary in-memory upload directory. The named volume remains writable.

`docker compose down` preserves the volume. **`docker compose down -v` deletes
accounts, keys, and dashboard configuration.** Do not delete it to upgrade.

## Networking

This Compose file uses Linux host networking to preserve the dashboard's strict
loopback checks and the OAuth callback on `127.0.0.1:1455`. The service binds only
to `127.0.0.1:8378`; there are no published ports or LAN dashboard access.
Port 1455 is used only during sign-in and must be free of other Codex login flows.
Docker Desktop is not a verified target for this configuration.

If port 8378 is occupied, create a `.env` file next to `compose.yaml`:

```dotenv
DICTATE_PORT=8388
```

Then run `docker compose up -d --build` and open
`http://127.0.0.1:8388/accounts`. Clients must use the same port. This changes
the dashboard/API port, not the fixed OAuth callback port.

For a remote Linux host, keep the service local and forward both ports:

```sh
ssh -N -L 8378:127.0.0.1:8378 -L 1455:127.0.0.1:1455 user@server
```

Open the local dashboard URL. If forwarding 1455 is unavailable, forward only
8378 and use the dashboard's callback-paste flow after browser sign-in.

## Build, inspect, update

```sh
docker compose build                  # creates codex-transcribe:local
docker compose images
docker compose ps
docker compose logs --tail=100
```

The multi-stage Dockerfile builds with Go 1.27.1 and runs a static binary in Alpine
with CA certificates. Health checks report process liveness, not ChatGPT login
or upstream availability. The integration uses ChatGPT's experimental private
transcription endpoint; use the dashboard transcription test to verify access.

To update from source while keeping your volume:

```sh
git pull --ff-only
docker compose up -d --build
```

To build the image separately:

```sh
docker build -t codex-transcribe:local .
```

# Production deployment

This single-instance setup runs the dashboard and consumer API behind Caddy with
automatic HTTPS. It uses Docker bridge networking, with no desktop keyring,
`auth.json`, host networking, or published OAuth callback port.

## Initial installation

Use a Linux server with Docker Engine and Docker Compose installed. Point a real
domain's DNS A/AAAA records to the server and allow inbound TCP ports 80 and 443.
Remove an AAAA record if the server cannot receive IPv6 traffic. These ports must
be available unless you use the existing-proxy option below.

From the repository directory:

```sh
cp production.env.example .env.production
# Edit .env.production: set DICTATE_DOMAIN to your domain, without https:// or a path.
install -d -m 0700 secrets
openssl rand -base64 24 > secrets/dashboard_password
chmod 0444 secrets/dashboard_password
docker compose --env-file .env.production -f compose.prod.yaml up -d --build --wait
```

The generated password is in `secrets/dashboard_password`; view it in your own
terminal to sign in. The file is readable by the non-root app through its
read-only Docker secret mount. Its parent directory is owner-only on the host.
Compose file-backed secrets are not an encrypted secret manager. Both `.env.*`
and `/secrets/` are ignored by Git and excluded from the image build context.
Do not regenerate the password file during an ordinary restart or update.

Open `https://YOUR_DOMAIN/accounts`. The app hashes the password before listening
on first startup, generates its session secret, and persists both in the app
volume. No public first-run password setup is offered. Passwords still support
4–256 characters. The bootstrap file initializes only a missing password: changing
it later does not overwrite an existing password.

Add your ChatGPT account, copy its login link, and open it in your browser. After
sign-in, the browser redirects to `http://127.0.0.1:1455/auth/callback?...` on your
computer. A failed connection there is expected for a remote deployment. Copy the
**entire URL from the address bar**, paste it into the dashboard callback field,
and choose **Complete sign-in**. It contains a single-use code: do not share it.
Avoid running a separate Codex login on your computer at the same time.

Test recording/transcription from the dashboard, then create a consumer API key.
Use `https://YOUR_DOMAIN/v1` as the client base URL and the generated key as the
Bearer token. Public transcription requires a consumer key even while the
dashboard is locked. A dashboard password/cookie does not authorize that API.

## Image and storage

The Dockerfile builds `codex-transcribe:production` locally. For a controlled
release, set `DICTATE_VERSION` to the reviewed commit/tag and `DICTATE_IMAGE` to
a unique tag before building. Keep that image for rollback. A registry image is
not published by this setup; if you build/push one in your own pipeline, set
`DICTATE_IMAGE` to its digest and deploy using `up -d --no-build --pull always`.
Digest references are for pulling; use a tag when building an image.

The `codex-dictate` Compose project owns these named volumes:

| Volume | Content |
| --- | --- |
| `dictate-data` | Account tokens, password hash, session secret, consumer key hashes |
| `caddy-data` | TLS certificates and private keys |
| `caddy-config` | Caddy runtime configuration |

Accounts are stored under `/data/config/codex-dictate/credentials.json` in the
app volume, unencrypted, mode `0600`. Treat volume snapshots and backups as
credentials. The app runs as UID/GID 10001 with a read-only root filesystem,
dropped capabilities, resource limits, and temporary uploads in memory.
`docker compose down` preserves volumes; **`down -v` deletes them**. Keep the
Compose project name stable so upgrades attach the same volumes. Do not run
multiple replicas against this credential store.

The backend has no published port in the bundled-proxy configuration. Caddy
preserves the public Host header; the app accepts only its configured HTTPS
origin, requires same-origin mutation requests, and issues Secure/HttpOnly
SameSite=Strict cookies. Do not expose the plain HTTP backend directly.

## Existing reverse proxy

If you already use Nginx, Caddy, or another proxy on the host:

```sh
docker compose --env-file .env.production \
  -f compose.prod.yaml -f compose.proxy.yaml up -d --build --wait
```

This disables bundled Caddy and publishes the app only at `127.0.0.1:8378`.
Configure your proxy's HTTPS virtual host for the exact `DICTATE_DOMAIN`, preserve
the original Host header, allow at least 25 MiB uploads plus multipart overhead,
and allow 120 seconds for a transcription response. Example Nginx location inside
an existing HTTPS server block:

```nginx
location / {
    proxy_pass http://127.0.0.1:8378;
    proxy_set_header Host $http_host;
    client_max_body_size 26m;
    proxy_read_timeout 120s;
}
```

TLS certificates for this option are managed by your existing proxy. A proxy in
another container must share a Docker network with the app and route to
`app:8378`; its own `127.0.0.1` does not refer to the host. The host-proxy override
above is intended for a proxy running directly on the host.

## Operations

Use the same env/Compose arguments for every command (include the proxy override
if you chose it):

```sh
docker compose --env-file .env.production -f compose.prod.yaml ps
docker compose --env-file .env.production -f compose.prod.yaml logs --tail=100
# Upgrade a reviewed source checkout, retaining all volumes:
docker compose --env-file .env.production -f compose.prod.yaml up -d --build --wait
```

Back up the app volume and bootstrap password file securely before upgrades;
stop the app during a volume snapshot for consistent account/configuration
files. Restore backups with UID/GID 10001 and their original permissions. Keep
certificate volumes too, to avoid unnecessary certificate reissuance.

The health check tests process liveness only. Verify account access with a real
dashboard transcription test after deployment. The ChatGPT login/transcription
integration uses an experimental private endpoint and may change independently
of this deployment. A global login-attempt cooldown is shared by users of this
single instance; add rate limits at your proxy if needed.

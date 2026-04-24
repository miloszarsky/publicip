# publicip

Lightweight service that returns your public IP address. Supports a modern browser UI, plain-text output for `curl`, and a JSON API.

## Features

- Detects IPv4 and IPv6
- Browser UI with copy-to-clipboard
- Plain-text response for CLI tools (`curl`, `wget`, `httpie`)
- JSON API at `/api`
- Health check endpoint at `/healthz`
- Trusted proxy validation (safe `X-Forwarded-For` handling)
- Graceful shutdown with connection draining
- Scratch-based Docker image (~11MB)
- Multi-arch: `linux/amd64`, `linux/arm64`

## Quick start

### Docker Compose (recommended)

```bash
git clone https://github.com/miloszarsky/publicip.git
cd publicip
docker compose up -d
```

### Docker (from GHCR)

```bash
docker run -d -p 3000:3000 \
  -e DOMAIN=ip.example.com \
  -e TRUSTED_PROXIES=172.16.0.0/12 \
  ghcr.io/miloszarsky/publicip:latest
```

### Binary

Download from [Releases](https://github.com/miloszarsky/publicip/releases) and run:

```bash
DOMAIN=ip.example.com TRUSTED_PROXIES=10.0.0.0/8 ./public-ip-linux-amd64
```

## Usage

```bash
# plain text
$ curl ip.rootik.cz
203.0.113.42

# JSON
$ curl -H "Accept: application/json" ip.rootik.cz
{"ip":"203.0.113.42","version":"IPv4"}

# JSON API endpoint
$ curl ip.rootik.cz/api
{"ip":"203.0.113.42","version":"IPv4"}
```

Open `http://localhost:3000` in a browser for the web UI.

## Configuration

All settings are controlled via environment variables:

| Variable | Default | Description |
|---|---|---|
| `PORT` | `3000` | Listen port |
| `BIND_ADDR` | `0.0.0.0` | Bind address |
| `DOMAIN` | `localhost` | Domain shown in the UI curl examples |
| `V4_DOMAIN` | *(empty)* | IPv4-only subdomain for dual-stack UI |
| `V6_DOMAIN` | *(empty)* | IPv6-only subdomain for dual-stack UI |
| `TITLE` | `public ip` | Page title and heading |
| `TRUSTED_PROXIES` | *(empty)* | Comma-separated CIDRs allowed to set `X-Forwarded-For`. Single IPs are accepted (e.g. `10.0.0.1`). When empty, proxy headers are ignored and `RemoteAddr` is used. |

## Dual-stack UI

To show both IPv4 and IPv6 addresses simultaneously in the browser, you need to provide subdomains that resolve only to their respective IP versions. This bypasses the browser's tendency to prefer one protocol over the other.

1. Create a subdomain (e.g., `v4.ip.example.com`) with only an **A record**.
2. Create another subdomain (e.g., `v6.ip.example.com`) with only an **AAAA record**.
3. Set the `V4_DOMAIN` and `V6_DOMAIN` environment variables.

**Note:** Ensure your reverse proxy (Nginx, Traefik, etc.) is configured to handle these subdomains and forward them to the service. For example, in Nginx, you should add them to the `server_name` directive:
```nginx
server_name ip.example.com v4.ip.example.com v6.ip.example.com;
```

The UI will then perform background requests to both domains to fetch and display both addresses.

## Reverse proxy setup

The service is designed to run behind a reverse proxy. Key points:

- Set `TRUSTED_PROXIES` to your proxy's IP/CIDR so the real client IP is extracted from `X-Forwarded-For`
- The compose file binds to `127.0.0.1:3000` by default (not publicly exposed)
- `/healthz` returns `200 ok` for proxy health checks

### Nginx example

```nginx
server {
    listen 80;
    server_name ip.rootik.cz;

    location / {
        proxy_pass http://127.0.0.1:3000;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }
}
```

### Traefik example (Docker labels)

```yaml
services:
  public-ip:
    image: ghcr.io/miloszarsky/publicip:latest
    labels:
      - traefik.enable=true
      - traefik.http.routers.publicip.rule=Host(`ip.rootik.cz`)
      - traefik.http.services.publicip.loadbalancer.server.port=3000
    environment:
      DOMAIN: ip.rootik.cz
      TRUSTED_PROXIES: "172.16.0.0/12"
```

## Releasing

Tag a version and push — the GitHub Actions pipeline builds binaries, creates a release, and pushes a multi-arch Docker image to GHCR:

```bash
git tag v1.1.0
git push origin v1.1.0
```

## License

[Apache License 2.0](LICENSE)

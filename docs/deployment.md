# Deployment

## Docker image

Pre-built images are published to GitHub Container Registry on every release.

```yaml
services:
  dsforms:
    image: ghcr.io/barancezayirli/dsforms:latest
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - dsforms_data:/data
    environment:
      - SECRET_KEY=your-secret-key
      - BASE_URL=https://forms.yourdomain.com
      - SMTP_HOST=smtp.example.com
      - SMTP_PORT=587
      - SMTP_USER=you@example.com
      - SMTP_PASS=your-password
      - SMTP_FROM=DSForms <noreply@example.com>

volumes:
  dsforms_data:
```

Pin a version (`ghcr.io/barancezayirli/dsforms:1.2.3`) or track `latest`.

Everything lives in the `/data` volume as a single SQLite file. Back that up and
you have backed up the whole instance.

## Reverse proxy

Put dsforms behind a proxy for TLS.

**Nginx**

```nginx
server {
    listen 443 ssl;
    server_name forms.example.com;

    ssl_certificate     /etc/letsencrypt/live/forms.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/forms.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

**Caddy**

```
forms.example.com {
    reverse_proxy localhost:8080
}
```

Set `BASE_URL=https://forms.example.com` so session cookies get the `Secure`
flag.

> **If you use IP or CIDR filter rules, the proxy is part of your security
> boundary.** dsforms trusts `X-Forwarded-For` for the client IP, so anything
> that can reach it directly can set that header to whatever it likes. Bind
> dsforms to localhost, as above, so only the proxy can reach it. Email and
> domain rules are unaffected — those match the sender field, which is validated
> rather than taken from a header.

## Health checks

`GET /healthz` returns `200 ok` when the process can reach its database, and
`503 database unavailable` when it cannot. It runs a real query rather than
checking that the server is listening — the failure worth catching is a process
that is up and answering every request with an error, which only a restart
fixes.

The Docker image ships a `HEALTHCHECK` that uses it, so `docker ps` reports
health and orchestrators restart the container on their own. Nothing needs
configuring.

It deliberately does not verify database *integrity*. A corrupt database is not
something a restart repairs, and failing a liveness probe on it turns a
damaged-but-serving instance into a crash loop.

What it does and does not catch, verified both ways: it catches a database
handle that has been closed — the state a failed restore could leave behind, and
the reason the endpoint exists. It does not catch the file being deleted out
from under a running process, because SQLite keeps the open inode and queries
keep succeeding against a file that no longer has a name.

# HTTPS and reverse proxies

Use a dedicated hostname, such as `files.example.com`, with a TLS reverse
proxy in front of Pigeon. Routes live at the origin root; mounting the
application under a prefix such as `/pigeon/` is not supported. Keep the HTTP
listener on loopback when the proxy runs on the same host.

The examples below assume a hostname pointing at the proxy and persistent
Pigeon data already initialized through the [installation guide](../getting-started.md).

## Sign-in throttling and reverse proxies

Each client may make 10 failed password attempts, through the sign-in form or
HTTP Basic authentication; one more attempt becomes available every 6 seconds.
Further attempts receive HTTP 429 with a `Retry-After` header before the
password is checked. Successful sign-ins don't count against the limit, so API
clients with valid credentials are never throttled. Clients are identified by
IPv4 address or IPv6 /64 network. At most one password check per CPU runs at a
time.

Behind a reverse proxy, every request arrives from the proxy's address. List the
proxy with `-trusted-proxies` so the server reads the client address from the
`X-Forwarded-For` header the proxy sets. It accepts comma-separated IP addresses
and CIDR ranges. The header is ignored on requests from any other address, so
clients cannot spoof it. List only the proxies' own addresses: a client
connecting from inside a trusted range could otherwise supply its own address.
Like `-dir` and `-listen`, the value is saved; `-trusted-proxies=` clears it.

```sh
go run ./cmd/pigeon -listen 127.0.0.1:8080 -trusted-proxies 127.0.0.1,::1
```

## Serving over HTTPS

Passwords, passphrases, and session cookies must not cross a network
unencrypted. Pigeon serves plain HTTP; put a TLS-terminating reverse proxy in
front of it and tell Pigeon the address people use:

```sh
go run ./cmd/pigeon -listen 127.0.0.1:8080 \
  -public-url https://files.example.com -trusted-proxies 127.0.0.1,::1
```

When `-public-url` uses HTTPS:

- every cookie is marked `Secure`;
- every response sends `Strict-Transport-Security: max-age=31536000`, even
  though the proxy forwards plain HTTP;
- form posts and uploads whose `Origin` is the public URL are accepted even if
  the proxy rewrites the `Host` header.

HSTS covers only this host, not its subdomains. Once browsers have seen it, they
refuse plain HTTP for the host for a year, so enable it only when HTTPS works.
The setting is saved like the others; `-public-url=` clears it. The URL must be
an origin without a path, because Pigeon's routes live at the root.

[Caddy](https://caddyserver.com) obtains and renews certificates automatically.
It keeps the `Host` header, replaces any client-supplied `X-Forwarded-For` with
the real client address, and streams uploads:

```caddyfile
files.example.com {
	reverse_proxy 127.0.0.1:8080
}
```

With nginx, remove the request body size limit, stream uploads so progress and
quota checks happen as data arrives, and set `X-Forwarded-For` to the client
address instead of appending to what the client sent:

```nginx
server {
    listen 443 ssl;
    server_name files.example.com;
    ssl_certificate     /etc/letsencrypt/live/files.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/files.example.com/privkey.pem;

    client_max_body_size 0;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_request_buffering off;
        proxy_read_timeout 1h;
    }
}
```

## Verify the deployment

Open the public HTTPS URL and check sign-in, a file upload, and a download.
Confirm the quota display and server progress update during a larger upload.
Create a share link and open it in a signed-out browser window, then revoke it
and check that a new download returns 404.

If a request fails through the proxy, compare its body limit, forwarding
headers, and timeouts with the examples above. See [troubleshooting](../troubleshooting.md)
for common status codes, and [operations](operations.md) for backups and upgrades.

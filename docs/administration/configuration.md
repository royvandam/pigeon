# Configuration

The SQLite database defaults to `./pigeon.db`. Select another location with
`-db /path/to/pigeon.db`. The database stores usernames, password hashes,
administrator roles, per-user disk quotas, login sessions, share links, and server settings.

On a new database, server settings default to `./uploads` and `127.0.0.1:8080`,
so only this machine can connect until you choose otherwise. Databases created
before this default keep their saved listen address. Use `-dir` or `-listen` to
change them. Overrides are saved in SQLite and reused on later starts without
those flags. The upload directory is stored as an absolute path. At startup the
server warns when it serves plain HTTP on an address other machines can reach;
see [Serving over HTTPS](deployment.md#serving-over-https).

```sh
go run ./cmd/pigeon -db ./pigeon.db -dir ./uploads -listen 127.0.0.1:8080
```

## Command-line reference

| Flag | Initial default | Saved in SQLite? | Purpose |
| --- | --- | --- | --- |
| `-db` | `./pigeon.db` | No | Database to open on this invocation. |
| `-dir` | `./uploads` | Yes | Upload root, stored as an absolute path. |
| `-listen` | `127.0.0.1:8080` | Yes | HTTP listen address in `host:port` form. |
| `-trusted-proxies` | Empty | Yes | Comma-separated IP addresses or CIDR ranges allowed to supply client addresses. |
| `-public-url` | Empty | Yes | Public HTTP or HTTPS origin, without a path, query, or fragment. |
| `-h`, `-help` | — | No | Print command-line help. |

Omitting a saved flag reuses its value from the selected database. Explicitly
pass `-trusted-proxies=` or `-public-url=` to clear either setting. Empty
`-dir` and `-listen` values leave their saved settings in place.

For example, after setting the upload root and listener above, this command
reuses them:

```sh
go run ./cmd/pigeon -db ./pigeon.db
```

## Paths and relocation

`-db` is resolved from the current working directory every time. A different
database path starts a separate installation and may prompt for a new
administrator. `-dir` is made absolute when set, so changing the working
directory later does not move existing uploads.

Changing `-dir` changes where Pigeon looks for uploads; it does not move files.
Stop the server, move the data, and restart with the new path as described in
[operations](operations.md). Keep the database and upload root together when
backing up or restoring.

## Proxy settings

`-public-url` controls HTTPS cookies, HSTS, and accepted browser origins. It
does not start a TLS listener. `-trusted-proxies` controls client address
discovery for sign-in throttling. Configure both when using an
[HTTPS reverse proxy](deployment.md).

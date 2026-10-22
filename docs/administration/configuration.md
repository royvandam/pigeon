# Configuration

Pigeon reads its settings from a JSON configuration file named on the command
line:

```sh
pigeon /etc/pigeon/pigeon.json
```

The repository includes
[`configs/pigeon.json`](https://github.com/royvandam/pigeon/blob/main/configs/pigeon.json),
which lists every setting with its default and keeps data in the working
directory. Copy it as a starting point. Settings take effect when Pigeon
starts; restart it after changing the file.

## Example

A service behind an HTTPS reverse proxy on the same host:

```json
{
  "database": {
    "path": "/srv/pigeon/pigeon.db"
  },
  "storage": {
    "local": {
      "directory": "/srv/pigeon/uploads"
    }
  },
  "http": {
    "listenAddress": "127.0.0.1:8080",
    "publicURL": "https://files.example.com",
    "trustedProxies": ["127.0.0.1", "::1"]
  }
}
```

## Settings

The file has one section for each part of Pigeon. Omitted settings take their
defaults; the database path and a storage provider are required.

### Database

| Setting | Default | Purpose |
| --- | --- | --- |
| `database.path` | Required | SQLite database file. Pigeon creates it, and missing parent folders, on first start. |

The database stores usernames, password hashes, administrator roles, per-user
disk quotas, login sessions, and share links.

### Storage

Choose exactly one storage provider. Pigeon currently provides `local`, which
keeps each account's files in a folder of the upload root.

| Setting | Default | Purpose |
| --- | --- | --- |
| `storage.local.directory` | Required | Upload root. Pigeon creates it at startup if needed. |

### HTTP

| Setting | Default | Purpose |
| --- | --- | --- |
| `http.listenAddress` | `127.0.0.1:8080` | Address and port of the plain HTTP listener, in `host:port` form. |
| `http.publicURL` | Empty | Public HTTP or HTTPS origin people use, without a path, query, or fragment. |
| `http.trustedProxies` | Empty | IP addresses or CIDR ranges of reverse proxies allowed to supply client addresses. |
| `http.formReadTimeout` | `30s` | Time a form body may take to arrive. |
| `http.uploadIdleTimeout` | `60s` | Time an upload may go without receiving data before Pigeon stops reading it. |

Durations are strings with a unit, such as `"30s"`, `"5m"`, or `"1m30s"`.

The default listener accepts connections only from the local machine. At
startup the server warns when it serves plain HTTP on an address other
machines can reach; see [Serving over HTTPS](deployment.md#serving-over-https).

## How the file is read

- Settings that Pigeon does not know are errors, so a misspelled name stops
  startup instead of being ignored. JSON has no comments; keep notes elsewhere.
- Pigeon checks the whole file before it opens the database or creates any
  folders, and names the section of a setting it rejects, such as
  `http: invalid listen address`.
- Relative paths are resolved from the working directory when Pigeon starts.
  Use absolute paths for a service. The upload root is logged at startup.

## Paths and relocation

A different database path selects a separate installation and may prompt for a
new administrator. Changing `storage.local.directory` changes where Pigeon
looks for uploads; it does not move files. Stop the server, move the data, and
restart with the new path as described in [operations](operations.md). Keep
the database, upload root, and configuration file together when backing up or
restoring.

## Proxy settings

`http.publicURL` controls HTTPS cookies, HSTS, and accepted browser origins. It
does not start a TLS listener. `http.trustedProxies` controls client address
discovery for sign-in throttling. Configure both when using an
[HTTPS reverse proxy](deployment.md).

## Upgrading from command-line flags

Earlier versions were configured with flags and saved all of them except `-db`
in the database:

| Flag | Setting |
| --- | --- |
| `-db` | `database.path` |
| `-dir` | `storage.local.directory` |
| `-listen` | `http.listenAddress` |
| `-trusted-proxies` | `http.trustedProxies`, as a list |
| `-public-url` | `http.publicURL` |

Flags are no longer accepted. Write a configuration file with the values the
installation used, and start Pigeon with it. On that first start, Pigeon
compares the settings saved in the database with the configuration. If any
differ, it refuses to start and lists each one with its saved and configured
values, so an upgrade cannot quietly serve another upload root or drop its
HTTPS and proxy settings:

```text
the database holds server settings saved by an earlier version of Pigeon, ...
	storage.local.directory: "/srv/pigeon/uploads" (configured: "/srv/pigeon/files")
	http.publicURL: "https://files.example.com" (configured: "")
```

Once they match, Pigeon removes them from the database and logs that it did.
From then on the configuration file alone decides, and the settings can be
changed. To see the saved values before upgrading, run
`sqlite3 pigeon.db 'SELECT * FROM settings'`.

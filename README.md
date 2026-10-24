# <img src="web/static/logo.webp" width="48" height="48" alt="" /> Pigeon

Pigeon is a self-hosted file sharing server written in Go. Upload and organize
files from your browser, download folders as ZIPs, share downloads with a link,
and collect files from others through upload links. Each account has its own
storage and disk quota; administrators manage accounts through a web interface.

## Quick start

Install Go **1.26.10 or newer**, or enter the development environment with
`nix-shell`, then run from the repository root:

```sh
go run ./cmd/pigeon configs/pigeon.json
```

Follow the terminal prompts to create the initial administrator, then open
<http://127.0.0.1:8080>. The password needs at least 12 characters and at most
72 bytes. Settings come from the JSON configuration file; the example keeps
data in `./pigeon.db` and `./uploads`. Accounts start with a 1 GiB quota.

For access from other devices, use an
[HTTPS reverse proxy](docs/administration/deployment.md).

## Documentation

Start with the [documentation overview](docs/index.md), or go directly to:

- [Installation and first run](docs/getting-started.md)
- [Files and uploads](docs/guide/files.md) and [sharing](docs/guide/sharing.md)
- [Accounts](docs/administration/accounts.md),
  [configuration](docs/administration/configuration.md), and
  [backups](docs/administration/operations.md)
- [HTTP API](docs/reference/api.md)
- [Development](docs/development/contributing.md)

Documentation lives in `docs/` and builds with MkDocs. See
[previewing and publishing the site](docs/development/documentation.md) for local
commands and GitHub Pages setup.

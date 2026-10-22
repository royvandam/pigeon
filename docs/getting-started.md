# Installation and first run

## Requirements

- Go **1.26.10 or newer**, as declared in
  [go.mod](https://github.com/royvandam/pigeon/blob/main/go.mod).
- A writable location for the SQLite database and upload directory.
- A terminal for the first administrator setup.

The optional `nix-shell` development environment supplies Go, `gopls`,
`goimports`, SQLite, Node.js, the GitHub CLI, and Python for documentation.
With Go's default `GOTOOLCHAIN=auto`, an older installed Go downloads the
required toolchain on first use. Without network access, install the required
version beforehand.

## Run from source

```sh
git clone https://github.com/royvandam/pigeon.git
cd pigeon
# Optional, when using Nix:
nix-shell
go run ./cmd/pigeon -listen 127.0.0.1:8080
```

Run commands from the repository root. On the first run, enter an administrator
username, a password, and its confirmation. The password needs at least 12
characters and at most 72 bytes; terminal password entry is hidden. Setup
finishes before the HTTP listener starts. Later starts reuse the saved accounts
and do not ask again.

Open <http://127.0.0.1:8080> and sign in. Try uploading a file, creating a
folder, and downloading an item. Open `/admin` to
[create another account](administration/accounts.md).

## Build a standalone executable

```sh
go build -o bin/pigeon ./cmd/pigeon
./bin/pigeon -db ./pigeon.db -dir ./uploads -listen 127.0.0.1:8080
```

Templates, browser assets, and the passphrase dictionary are embedded at build
time. The executable can run without the source tree. It still needs its
database and upload directory; keep both on persistent storage.

## Choose where data lives

The defaults are `./pigeon.db`, `./uploads`, and `127.0.0.1:8080`. The database
path is relative to the working directory; use absolute paths for a service:

```sh
./bin/pigeon -db /srv/pigeon/pigeon.db \
  -dir /srv/pigeon/uploads -listen 127.0.0.1:8080
```

`-dir` and `-listen` are saved in that database. Later starts need only the same
`-db` argument. The initial administrator, like other accounts, starts with a
1 GiB quota. See [configuration](administration/configuration.md) for all flags
and [disk quotas](administration/quotas.md) for how usage is counted.

## Automated provisioning

On an empty database, setup also accepts three newline-separated lines on
standard input: username, password, and password confirmation. Provisioning
tools can supply these from their secret store. Avoid putting real passwords
in command arguments, shell history, or checked-in scripts. Once the database
has an account, setup no longer reads these values.

## Access from other devices

The default listener accepts connections only from the local machine. To
serve other devices, follow [HTTPS and reverse proxies](administration/deployment.md).
Pigeon itself serves HTTP; the reverse proxy terminates TLS. Set
`-public-url` to the HTTPS origin before using accounts over the network.

For common setup and upload problems, see [troubleshooting](troubleshooting.md).

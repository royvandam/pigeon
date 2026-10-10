# Upload

A Go HTTP server with individual user accounts, an administrator management page,
and streaming uploads stored in a separate folder for each user.

Enter the development environment with `nix-shell`. It provides Go 1.26, `gopls`,
`goimports`, and the SQLite CLI. Go 1.26 or newer also works without Nix.

```sh
go run . -listen 127.0.0.1:8080
```

On the first run, the terminal asks for the initial administrator's username,
password, and password confirmation. Password entry is hidden in a terminal.
Passwords need at least 8 characters and may contain at most 72 bytes. Setup
finishes before the HTTP listener starts. Subsequent starts use the existing
accounts and do not prompt again. First-run input can also be supplied on stdin
as three lines for automated provisioning.

Open http://127.0.0.1:8080 and sign in. Administrators can open `/admin` to create
regular users or additional administrators, delete users, and reset passwords.
Regular users can upload files but cannot access user management. An administrator
cannot delete their own account or the last administrator. Resetting a password
signs that user out on every device; resetting your own password returns you to
the login page.

## Configuration

The SQLite database defaults to `./uploader.db`. Select another location with
`-db /path/to/uploader.db`. The database stores usernames, password hashes,
administrator roles, login sessions, and server settings.

On a new database, settings default to `./uploads`, a 200 MiB file limit, and
`0.0.0.0:8080`. Use `-dir`, `-max`, or `-listen` to change them. Overrides are saved
in SQLite and reused on later starts without those flags. The upload directory
is stored as an absolute path. Size suffixes such as `K`, `MB`, and `GB` use powers
of 1024. Another 10 MiB is allowed for multipart overhead.

```sh
go run . -db ./uploader.db -dir ./uploads -max 500MB -listen 127.0.0.1:8080
```

## Upload folders and account deletion

An upload from `Roy Van Dijk` is saved in `uploads/roy-van-dijk/`. Names are
normalized with Unicode case folding, accents are removed where possible, and
punctuation and whitespace become hyphens. Other Unicode letters and digits are
retained. Username comparisons are case-insensitive. Creation fails if the
username or normalized folder name is already reserved.

Deleting an account disables access immediately and invalidates its sessions.
Its files remain on disk, and a small deleted account record reserves its
username and folder so a future account cannot inherit those uploads. Files
uploaded by the previous shared-password implementation remain in the upload
root; they are not automatically assigned to an account.

## API

`POST /upload` accepts `multipart/form-data` with a part named `file`. Authenticate
using HTTP Basic authentication with a username and password:

```sh
curl --user 'Roy Van Dijk' --form 'file=@document.pdf' http://127.0.0.1:8080/upload
```

Curl prompts for the password. Successful responses include `saved_as` and
`bytes`. The first part named `file` is saved; subsequent parts are ignored.
The shared `-password` flag and `X-Upload-Password` header have been removed.

The browser uses an HttpOnly session cookie valid for 12 hours. Forms require
a CSRF token, and browser uploads send it in the `X-CSRF-Token` header. Session
tokens are stored as hashes. Passwords use bcrypt. Use HTTPS when serving the
application beyond a trusted local network.

## Development

```sh
go test ./...
go test -race ./...
go vet ./...
go build -o uploader .
```

Run and build the whole package (`.`). All pages in `templates/` are embedded in
the binary. The browser loads Tailwind CSS from a CDN.

`main.go` handles startup, `database.go` persists settings and the schema,
`setup.go` handles first-run provisioning, `users.go` manages accounts, and
`session.go` and `auth.go` handle authentication. `admin.go`, `index.go`, and
`upload.go` serve the management, upload page, and upload endpoint. `server.go`
handles routing and shutdown; size and filename helpers remain separate.
SIGINT and SIGTERM allow active requests up to 10 seconds to finish.

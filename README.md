# Pigeon

Pigeon moves files and data between devices. It is a Go HTTP server with
individual user accounts, an administrator management page, and streaming
uploads stored in a separate folder for each user.

Enter the development environment with `nix-shell`. It provides Go, `gopls`,
`goimports`, the SQLite CLI, Node.js for upload-page tests, and the GitHub CLI
(`gh`). `go.mod` requires Go 1.26.9 or newer for its security fixes. With Go's
default `GOTOOLCHAIN=auto`, an older Go, including the Nix shell's, downloads
that release automatically on first use. Without Nix, install Go 1.26.9 or newer.

```sh
go run ./cmd/pigeon -listen 127.0.0.1:8080
```

On the first run, the terminal asks for the initial administrator's username,
password, and password confirmation. Password entry is hidden in a terminal.
Passwords need at least 12 characters and may contain at most 72 bytes. Setup
finishes before the HTTP listener starts. Subsequent starts use the existing
accounts and do not prompt again. First-run input can also be supplied on stdin
as three lines for automated provisioning.

Open http://127.0.0.1:8080 and sign in. Administrators can open `/admin` to create
regular users or additional administrators, assign and change disk quotas, delete
users, and reset passwords. Creating a user or resetting a password generates
a random four-word passphrase, separated by hyphens, and shows it on the
result page. Copy it before leaving that page; it cannot be retrieved
later. Only its bcrypt hash is stored. User deletion uses the same confirmation
dialog as file deletion and preserves the user's uploaded files.
Deleted accounts appear under **Deleted users**. **Recycle account** asks for
confirmation, restores the account's role, quota, and uploaded files, and
displays a fresh four-word passphrase. Old passwords, sessions, and share links
remain invalid. Creating a user with a deleted account's username or normalized
folder name offers this recycling action instead of just reporting a conflict.
**Purge account** permanently removes a deleted account and all its uploaded
files and folders after confirmation. Its username and normalized folder name
can then be used for a new account with empty storage. Purging is available only
for deleted accounts; active users must be deleted first.
Regular users can manage their own files but cannot access user management. An administrator
cannot delete their own account or the last administrator. **Reset password**
opens a confirmation dialog; Cancel or Escape leaves the password and sessions
unchanged. Confirming the reset signs that user out on every device. Resetting
your own password shows the new passphrase on a signed-out result page with a
link to sign in again.

## File explorer and uploads

The home page lists your files and folders with sizes and modification times.
Open a folder to browse its contents; the breadcrumb links return to its parents.
Create folders, rename items, move them to another existing folder, or delete
them. Rename, move, and delete use a shared in-page dialog. **Save** applies an
edit, **Delete** confirms deletion, and **Cancel** closes any dialog without
making changes. Escape dismisses a visible validation tooltip first; otherwise
it closes the dialog. Moving asks for a path relative to **My files**, with an
empty path meaning the root. Deleting a folder removes its contents too. Empty
folder names and rename fields show styled tooltips without shifting the layout.
Files download as attachments, and folders download as ZIP archives, including
empty subfolders.

Use **Choose files** to select one or several files, or **Or choose a directory**
to upload a directory and all files in its subfolders. The page shows the number
of selected files, their combined size, and progress for the whole upload.

Uploads go into the folder currently open in the explorer. Original filenames
(including spaces, Unicode, and dotfiles) and directory hierarchies are preserved,
without timestamped filenames or wrapper folders. For example, uploading
`project/docs/readme.txt` while browsing `Archive` saves it as
`uploads/roy-van-dijk/Archive/project/docs/readme.txt`.
Directory uploads can add files to existing folders. Existing files are never
replaced: a filename conflict rejects the whole upload with HTTP 409. Rename,
move, or delete the existing item before uploading a replacement. Previously
uploaded timestamped files and folders remain accessible in the explorer.
The directory picker includes files; empty directories are not created.

The combined file size must fit the user's remaining quota. Each batch is
streamed to a temporary directory and published only after the entire request
succeeds. Invalid paths, duplicate paths, interrupted requests, or quota failures
remove the whole temporary batch and preserve earlier uploads.

Upload progress follows file bytes processed by the server, including when a
proxy buffers the browser's request. The bar reaches 100% only after the server
confirms a successful upload. Speed and the approximate time remaining use a
rolling five-second window of server progress, excluding multipart overhead.
Queued uploads and storage checks show their current phase; final saving shows
**Finishing…** until confirmed. A stalled transfer stops reporting a countdown.

## Shareable downloads

Choose **Share** beside a file or folder, then **Create link** and **Copy**.
Anyone with that URL can download the item without an account. Files download
as attachments; folders download as ZIPs containing their current contents,
including files added after the link was created. Opening the Share dialog does
not enable sharing until you choose **Create link**.

Each item has at most one link. Links survive restarts and do not expire
automatically. Only a SHA-256 hash of each link's token is stored, like session
tokens, so a leaked database or backup does not reveal working links. The URL
is therefore shown only once, right after **Create link**; copy it then.
Opening **Share** again shows that a link is active and offers **Replace link**,
which creates a new URL and disables the old one, or **Revoke link**. If
clipboard access is unavailable, the dialog selects the URL so you can copy it
manually. Databases from earlier versions are upgraded on start: their links
keep working, and their tokens are replaced by hashes.

Renaming, moving, or deleting an item revokes links to that item and its
descendants, so recreating an old path cannot expose replacement files. Deleting
an account also revokes all its links while preserving its uploaded files.
Password resets leave sharing links active. Revocation blocks new downloads;
downloads already in progress can finish. Public tokens are omitted from the
application's request logs.

## Configuration

The SQLite database defaults to `./pigeon.db`. Select another location with
`-db /path/to/pigeon.db`. The database stores usernames, password hashes,
administrator roles, per-user disk quotas, login sessions, share links, and server settings.

On a new database, server settings default to `./uploads` and `127.0.0.1:8080`,
so only this machine can connect until you choose otherwise. Databases created
before this default keep their saved listen address. Use `-dir` or `-listen` to
change them. Overrides are saved in SQLite and reused on later starts without
those flags. The upload directory is stored as an absolute path. At startup the
server warns when it serves plain HTTP on an address other machines can reach;
see [Serving over HTTPS](#serving-over-https).

```sh
go run ./cmd/pigeon -db ./pigeon.db -dir ./uploads -listen 127.0.0.1:8080
```

### Sign-in throttling and reverse proxies

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

### Serving over HTTPS

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


## Disk quotas

Every account, including the initial administrator, defaults to `1GB` of total
stored files (1 GiB, or 1,073,741,824 bytes). Admins can supply another quota when
creating a user and edit existing users' quotas on `/admin`. Quotas must be
positive; values such as `500MB`, `2GB`, or `100B` use powers of 1024.

The upload page shows used, total, and remaining storage and updates these values
after an upload. The server measures everything inside each user's folder,
including files already on disk and files in subfolders. It measures a folder
once, when the account is first used after the server starts, and then keeps the
result up to date as files change through Pigeon. Files added or removed
directly on disk show within 10 minutes. Uploads cut off by a server stop are
removed at the next start. Like disk allocation, usage is counted in 4 KiB units:

- Each file's size is rounded up to a whole unit, with a minimum of one unit even
  when the file is empty.
- Each folder takes one unit.

For example, a 5,000-byte file uses 8 KiB and an empty folder uses 4 KiB. As a
result, a quota also limits how many files and folders an account can create:
a 1 GiB quota allows at most 262,144. Removing files or folders frees quota.
Multipart overhead is excluded from disk usage; requests allow up to another
10 MiB for multipart data. The `bytes` value in upload responses still reports
the files' actual sizes.

Uploads exceeding the remaining quota receive HTTP 413, and the entire failed
batch is removed. Creating a folder also needs one free unit and otherwise
returns HTTP 413. The upload page checks a selection against the remaining
quota with the same rounding before sending it. Uploads, file changes, and quota edits for the same user are serialized in the
running server; different users can upload simultaneously. Run one server process per
upload root. A quota may be reduced below current usage: existing files are
preserved and new uploads are blocked until usage falls below the quota or an
admin raises it.

Existing databases are migrated automatically: all existing users receive `1GB`,
and the old global upload limit and `-max` flag are removed. Accounts, sessions,
uploads, and the other server settings are preserved.

## Upload folders and account deletion

An upload from `Roy Van Dijk` is saved in `uploads/roy-van-dijk/`. Names are
normalized with Unicode case folding, accents are removed where possible, and
punctuation and whitespace become hyphens. Other Unicode letters and digits are
retained. Username comparisons are case-insensitive. Creation fails if the
username or normalized folder name is already reserved.

Deleting an account disables access immediately and invalidates its sessions and
share links.
Its files remain on disk, and a small deleted account record reserves its
username and folder until an administrator explicitly recycles or purges that
account.
Recycling preserves the account's identity and files while issuing a new
passphrase; it does not reactivate previous sessions or public links. Purging
permanently deletes the account and its uploads, including empty folders and
unfinished upload files, and releases its username and normalized folder name.
If file removal fails, the account stays deleted and its name stays reserved;
some files may already have been removed. Correct the storage problem and retry
the purge to finish it. Files uploaded by the previous shared-password
implementation remain in the upload root; they are not automatically assigned
to an account.

## API

`POST /upload` accepts `multipart/form-data` with one or more parts named `file`.
Add `?dir=Archive/docs` to upload into an existing folder relative to your root;
omitting `dir` uploads to the root.
Authenticate using HTTP Basic authentication with a username and password:

```sh
curl --user 'Roy Van Dijk' --form 'file=@document.pdf' http://127.0.0.1:8080/upload
```

Curl prompts for the password and sends it with the first request. Requests
without valid credentials receive HTTP 401 with a Basic challenge, unless they
identify as coming from a browser through Fetch Metadata headers,
`Upgrade-Insecure-Requests`, or `X-Requested-With: XMLHttpRequest`. A browser
would answer the challenge with its own sign-in prompt and then send that
password with every request, bypassing the CSRF check and Sign out. Browsers
instead get a plain 401, and page loads are redirected to the sign-in page.

To preserve a directory hierarchy, set each part's
filename to its slash-separated relative path:

```sh
curl --user 'Roy Van Dijk' \
  --form 'file=@project/readme.txt;filename=project/readme.txt' \
  --form 'file=@project/docs/guide.pdf;filename=project/docs/guide.pdf' \
  http://127.0.0.1:8080/upload
```

Successful responses include `saved_as` (the file's relative path for a single
individual file, or the destination folder for a batch; `.` means the root) and
`bytes` (the sum of all file sizes). Directory and multiple-file responses also
include `files` (the number of saved files). Responses include `X-Quota-Limit`,
`X-Quota-Used`, and `X-Quota-Remaining` headers (bytes). All parts named `file` are
saved. Paths must be relative, use `/` separators, and contain no empty, `.` or
`..` components, backslashes, Windows drive prefixes, or control characters. File and directory
components may contain at most 255 bytes; full paths at most 4095 bytes. Names
starting with `.upload-` are reserved for private staging and cannot be accessed.
Uploaded paths, new folders, and renames also cannot contain invisible
formatting characters (Unicode category Cf), such as right-to-left overrides
that make `invoice`, U+202E, `fdp.exe` display as `invoiceexe.pdf`. Zero-width joiners and
non-joiners are allowed, because some scripts and emoji need them. Items stored
earlier with such names remain accessible, and downloads suggest their names
without these characters.
Duplicate or conflicting paths within a request return HTTP 400. Conflicts with
existing files or folders return HTTP 409 without saving any of the batch.

An upload fails with HTTP 408 when no data arrives for 60 seconds; form
submissions, including sign-in, must arrive within 30 seconds. An account's
uploads are received one at a time, but its pages, share links, and file actions
do not wait for them. The quota, destination folder, and account are checked
again before an upload is published, so changes made meanwhile are respected.

`GET /?dir=Archive/docs` opens the explorer. `GET /files?dir=Archive/docs`
returns its HTML file-list fragment. `GET /download?path=Archive/docs/report.pdf`
downloads a file; a directory path returns a ZIP, and an empty path downloads
the entire user folder. These endpoints require authentication and can only
access the authenticated user's files. Symbolic links are rejected.

`GET /shares?path=Archive/docs/report.pdf` returns `{"url":"","active":true}`
when the item has a link, or `"active":false` when it does not; the URL of an
existing link is not stored and cannot be returned. `POST /shares` accepts
URL-encoded `action=create` or `action=revoke`, plus `path`. `create` returns
`{"url":"/s/<token>","active":true}` with a new link, replacing any existing
link for that item. Both endpoints require authentication and manage only the
signed-in user's links. Session-authenticated POSTs also require `csrf_token`, like other
file operations. The path must name a file or folder; sharing the account root
is not supported.

`GET /s/<token>` and `HEAD /s/<token>` provide the shared download without
authentication. File downloads support byte ranges. Query parameters cannot
change the shared item. Invalid or revoked links return HTTP 404, and these
URLs allow downloads only.

`POST /files` accepts URL-encoded forms with `action` and an optional `dir`
(the folder to return to). Actions are:

| Action | Fields | Behavior |
| --- | --- | --- |
| `mkdir` | `name` | Create a folder inside `dir`. |
| `rename` | `path`, `name` | Give an item a new name in its current folder. |
| `move` | `path`, `destination` | Move an item into an existing folder; empty destination means root. |
| `delete` | `path` | Delete a file or a folder and its contents. |

`path` and `destination` are relative to the user's root, not to `dir`.
Successful changes redirect to the explorer. Renames and moves reject existing
destinations with HTTP 409. The user root cannot be renamed, moved, or deleted.

For example, create a destination and upload there:

```sh
curl --user 'Roy Van Dijk' --data-urlencode 'action=mkdir' \
  --data-urlencode 'name=Archive' http://127.0.0.1:8080/files
curl --user 'Roy Van Dijk' --form 'file=@document.pdf' \
  'http://127.0.0.1:8080/upload?dir=Archive'
```

To track progress, send a unique `X-Upload-ID` header (16–64 ASCII letters, digits,
underscores, or hyphens) with the upload, then poll
`GET /upload-progress?id=<upload-id>` with the same account's authentication.
The JSON response has `bytes_received` (file bytes written so far) and `state`
(`queued`, `preparing`, `uploading`, `finalizing`, `complete`, or `failed`).
Progress records are held in memory and expire five minutes after completion or
failure; other users cannot read them. Reusing an existing ID returns HTTP 409.
The header is optional for clients that do not need progress.

The shared `-password` flag and `X-Upload-Password` header have been removed.

The browser uses an HttpOnly session cookie valid for 12 hours. Forms require
a CSRF token, and browser uploads send it in the `X-CSRF-Token` header. Session
tokens are stored as hashes. Passwords use bcrypt. Serve the application over
HTTPS whenever other machines can reach it; see
[Serving over HTTPS](#serving-over-https).

Every response sends a Content Security Policy that allows only same-origin
scripts, styles, fonts, and requests, and forbids inline code, plugins, base-URL
changes, form posts to other sites, and framing. Responses also send
`X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, and
`Referrer-Policy: same-origin`; public share links use `no-referrer`. Templates
must not contain inline scripts, `style` attributes, or event handler
attributes. A test enforces this.

## Project layout

The repository uses the applicable directory conventions from
[`golang-standards/project-layout`](https://github.com/golang-standards/project-layout):

```text
cmd/pigeon/         Executable entry point and signal handling
internal/pigeon/    Private application code and Go tests
  wordlists/        Embedded passphrase dictionary and attribution
web/                Embedded templates and browser assets
  static/           JavaScript, shared and generated CSS
    fontawesome/    Vendored Font Awesome Free styles, font, and license
  templates/        Server-rendered HTML
test/browser/       Browser-script regression tests
scripts/            Development tooling helpers
```

Go tests stay beside the packages they exercise; additional browser tests live
under `test/`. The `web` package embeds templates and static files at build time,
and the application embeds its passphrase word list. A built executable runs
without the source tree or an asset directory. There is no public Go library;
application code stays under `internal/`.

## Development

### VS Code over SSH

Install the Go (`golang.go`) and
[Workspace Shared Settings](https://github.com/bohdan-shulha/vs-wss)
(`b-shulha.wss`) extensions on the remote SSH host. Open this project and run
**Workspace Shared Settings: Apply Workspace Settings** from the Command
Palette, then choose **Restart** when prompted, or run **Developer: Reload
Window**. The workspace must be trusted for Go to use its tool settings.

The extension expands `.vscode/settings.workspace.json` into the local,
Git-ignored `.vscode/settings.json`. The helper in `scripts/vscode-settings.sh`
enters this project's `shell.nix` on the remote host to discover the absolute
paths of `go`, `gopls`, and `goimports`. It also supplies the Nix shell's `PATH` through
`go.toolsEnvVars`, allowing `gopls` to find Go even when the SSH extension host
started outside `nix-shell`. Opening a Nix shell in the integrated terminal
alone does not update the running extension host's environment.

Go tools use `/tmp` for temporary files. Nix Env Selector can copy `TMPDIR`,
`TMP`, and `TEMP` from a short-lived Nix shell into the extension host, even
though that shell's temporary directory is removed when it exits. Overriding
these variables in `go.toolsEnvVars` prevents `go list` from failing with
`go: creating work dir: stat /tmp/nix-shell-…: no such file or directory` and
`gopls` from reporting **No packages found for open file**. After applying the
workspace settings, run **Go: Restart Language Server** to reload its environment.

Keep shared settings in `settings.workspace.json`; command substitutions are
expanded by Workspace Shared Settings rather than VS Code itself. Reapply the
workspace settings after changing `shell.nix` or updating your Nix packages.
Use **Go: Locate Configured Go Tools** to check the resolved paths.

### Application development

Account changes are coordinated by `accountManager` in `accounts.go`; the SQLite
store owns persistence and transactions. `userStorage` owns account filesystem
paths, usage checks, and removal. `accountAccess` locks an account and rechecks
its identity before both reads and writes; download handles release that lock
before streaming. Uploads hold the lock only to check the quota and to publish:
while the body streams into the staging folder they hold the account's upload
slot instead, which keeps uploads to one account one at a time. Purging and
recycling take the slot as well. Request body deadlines live in `deadline.go`.

HTTP handlers parse requests and render responses, using shared method,
authentication, and error helpers. Explorer listing and file mutation rules live
in `explorer.go` and `file_management.go`. Template loading is in `templates.go`;
templates reuse CSRF and sign-out controls. Browser code and common styles live
in `web/static/` and are embedded into the binary along with the templates.
`ActionDialog` uses one definition per action and one controller for all dialogs.
JavaScript tests run the same files served by the application. Rebuild and restart
after changing templates or assets.

Go tests run in parallel and hash passwords at bcrypt's minimum cost, so the
suite takes seconds; only a test that replaces the global log output stays
serial. Helpers shared by several test files live in
`internal/pigeon/helpers_test.go`.

```sh
gofmt -l .
go mod tidy -diff
go test ./...
go test -race ./...
go vet ./...
node --test --test-isolation=none test/browser/*.test.cjs
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
go build -o bin/pigeon ./cmd/pigeon
```

GitHub Actions runs these checks for every pull request and push to `main`, and
runs `govulncheck` weekly so new advisories surface without code changes. CI
uses the Go version declared in `go.mod`. Dependabot proposes grouped weekly
updates for Go modules and the pinned actions once a release is a week old.
Dependabot does not update Go itself: when `govulncheck` reports a
standard-library vulnerability, raise the `go` line in `go.mod` to the patch
release that fixes it.

Run and build the command package (`./cmd/pigeon`) from the repository root.
Build output belongs in the ignored `bin/` directory.

### Styles and icons

Pages load only assets embedded in the binary; `web/templates/assets.html`
links them and no page contacts a CDN. `web/static/tailwind.css` is generated
from the Tailwind classes in `web/templates/` and `web/static/*.js`. After
adding or changing classes, regenerate and commit it:

```sh
go generate ./web
```

This runs `scripts/tailwindcss.sh`, which downloads the pinned Tailwind CSS
standalone CLI on first use, verifies its SHA-256 checksum, and caches it under
`~/.cache/pigeon`. Font Awesome Free 7.1.0 is vendored in
`web/static/fontawesome/`: the core and solid styles, the solid font, and the
license. `scripts/vendor-fontawesome.sh` recreates these files from the
checksum-verified npm release. CI reruns both and fails if the committed files
differ. Dependabot does not track either version; to upgrade, change the version
and checksums in the script, rerun it, and commit the result.

`web/static/styles.css` sets fields, labels, buttons, and links to a consistent
14px font size and 20px line height, including file picker buttons.
The dialog and sharing templates supply markup; `web/static/dialog.js` and
`web/static/shares.js` handle interaction. `web/static/validation.js` replaces
native validation popups across all forms with shared, styled tooltips that do
not shift the layout, while keeping browser constraints and server-side checks.

`cmd/pigeon/main.go` handles signals and invokes `pigeon.Run`.
Inside `internal/pigeon`, `run.go` handles startup, `database.go` persists
settings and the schema, `setup.go` handles first-run provisioning, and
`users.go` manages accounts. `passphrase.go` generates credentials using the
embedded EFF word list. Its source and license are documented in
[`internal/pigeon/wordlists/README.md`](internal/pigeon/wordlists/README.md).
`session.go` and `auth.go` handle authentication. `admin.go`, `index.go`, and
`upload.go` serve user management, the explorer page, and the upload endpoint.
`files.go` handles explorer requests, and `download.go` serves files and ZIP
archives. `upload_files.go` streams and publishes batches. `server.go` handles
routing and shutdown; `quota.go` enforces quotas, and `storage_usage.go` measures
usage. `upload_progress.go` tracks uploads and serves authenticated progress
snapshots.
Size and filename helpers remain separate.
SIGINT and SIGTERM allow active requests up to 10 seconds to finish.

## AI assistance

Pigeon is developed with the help of Claude Code, an AI coding assistant.

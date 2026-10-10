# Upload

A Go HTTP server with individual user accounts, an administrator management page,
and streaming uploads stored in a separate folder for each user.

Enter the development environment with `nix-shell`. It provides Go 1.26, `gopls`,
`goimports`, the SQLite CLI, and Node.js for upload-page tests. Go 1.26 or newer
also works without Nix.

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
regular users or additional administrators, assign and change disk quotas, delete
users, and reset passwords.
Regular users can upload files but cannot access user management. An administrator
cannot delete their own account or the last administrator. Resetting a password
signs that user out on every device; resetting your own password returns you to
the login page.

## Uploading directories

Use **Choose files** to select one or several files, or **Or choose a directory**
to upload a directory and all files in its subfolders. The page shows the number
of selected files, their combined size, and progress for the whole upload.

Directory uploads preserve filenames (including spaces, Unicode, and dotfiles)
and the folder hierarchy inside a unique upload folder. For example,
`project/docs/readme.txt` is stored as
`uploads/roy-van-dijk/<timestamp>_<random>/project/docs/readme.txt`.
Selecting several individual files also saves them together in a unique folder.
A single individual file keeps the existing timestamped, sanitized filename.
Repeated uploads create separate destinations and do not overwrite earlier files.
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

## Configuration

The SQLite database defaults to `./uploader.db`. Select another location with
`-db /path/to/uploader.db`. The database stores usernames, password hashes,
administrator roles, per-user disk quotas, login sessions, and server settings.

On a new database, server settings default to `./uploads` and `0.0.0.0:8080`.
Use `-dir` or `-listen` to change them. Overrides are saved in SQLite and reused
on later starts without those flags. The upload directory is stored as an absolute path.

```sh
go run . -db ./uploader.db -dir ./uploads -listen 127.0.0.1:8080
```

## Disk quotas

Every account, including the initial administrator, defaults to `1GB` of total
stored files (1 GiB, or 1,073,741,824 bytes). Admins can supply another quota when
creating a user and edit existing users' quotas on `/admin`. Quotas must be
positive; values such as `500MB`, `2GB`, or `100B` use powers of 1024.

The upload page shows used, total, and remaining storage and updates these values
after an upload. The server measures all regular file sizes inside each user's
folder, including files already on disk and files in subfolders. Removing files
from the folder frees quota. Multipart overhead is excluded from disk usage;
requests allow up to another 10 MiB for multipart data.

Uploads exceeding the remaining quota receive HTTP 413, and the entire failed
batch is removed. Uploads for the same user and quota edits are serialized in the
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

Deleting an account disables access immediately and invalidates its sessions.
Its files remain on disk, and a small deleted account record reserves its
username and folder so a future account cannot inherit those uploads. Files
uploaded by the previous shared-password implementation remain in the upload
root; they are not automatically assigned to an account.

## API

`POST /upload` accepts `multipart/form-data` with one or more parts named `file`.
Authenticate using HTTP Basic authentication with a username and password:

```sh
curl --user 'Roy Van Dijk' --form 'file=@document.pdf' http://127.0.0.1:8080/upload
```

Curl prompts for the password. To preserve a directory hierarchy, set each part's
filename to its slash-separated relative path:

```sh
curl --user 'Roy Van Dijk' \
  --form 'file=@project/readme.txt;filename=project/readme.txt' \
  --form 'file=@project/docs/guide.pdf;filename=project/docs/guide.pdf' \
  http://127.0.0.1:8080/upload
```

Successful responses include `saved_as` (the saved filename or batch folder) and
`bytes` (the sum of all file sizes). Directory and multiple-file responses also
include `files` (the number of saved files). Responses include `X-Quota-Limit`,
`X-Quota-Used`, and `X-Quota-Remaining` headers (bytes). All parts named `file` are
saved. Paths must be relative, use `/` separators, and contain no empty, `.` or
`..` components, backslashes, Windows drive prefixes, or control characters. File and directory
components may contain at most 255 bytes; full paths at most 4095 bytes. Duplicate
paths and file/directory conflicts return HTTP 400 without saving any of the batch.

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
tokens are stored as hashes. Passwords use bcrypt. Use HTTPS when serving the
application beyond a trusted local network.

## Development

```sh
go test ./...
go test -race ./...
go vet ./...
node --test --test-isolation=none tests/*.test.cjs
go build -o uploader .
```

Run and build the whole package (`.`). All pages in `templates/` are embedded in
the binary. The browser loads Tailwind CSS from a CDN.

`main.go` handles startup, `database.go` persists settings and the schema,
`setup.go` handles first-run provisioning, `users.go` manages accounts, and
`session.go` and `auth.go` handle authentication. `admin.go`, `index.go`, and
`upload.go` serve the management, upload page, and upload endpoint.
`upload_files.go` streams and publishes batches. `server.go` handles routing and
shutdown; `quota.go` measures storage and enforces quotas. `upload_progress.go`
tracks uploads and serves authenticated progress snapshots.
Size and filename helpers remain separate.
SIGINT and SIGTERM allow active requests up to 10 seconds to finish.

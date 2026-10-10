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
users, and reset passwords. Creating a user generates a random four-word
passphrase, separated by hyphens, and shows it to the administrator on the
creation result page. Copy it before leaving that page; it cannot be retrieved
later. Only its bcrypt hash is stored. User deletion uses the same confirmation
dialog as file deletion and preserves the user's uploaded files.
Deleted accounts appear under **Deleted users**. **Recycle account** asks for
confirmation, restores the account's role, quota, and uploaded files, and
displays a fresh four-word passphrase. Old passwords, sessions, and share links
remain invalid. Creating a user with a deleted account's username or normalized
folder name offers this recycling action instead of just reporting a conflict.
Regular users can manage their own files but cannot access user management. An administrator
cannot delete their own account or the last administrator. Resetting a password
signs that user out on every device; resetting your own password returns you to
the login page.

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

Each item has one reusable link. Links are stored in SQLite, survive restarts,
and do not expire automatically. Open **Share** again to copy the existing link
or choose **Revoke link**. Creating another link after revocation generates a
different URL; the old URL remains disabled. If clipboard access is unavailable,
the dialog selects the URL so you can copy it manually.

Renaming, moving, or deleting an item revokes links to that item and its
descendants, so recreating an old path cannot expose replacement files. Deleting
an account also revokes all its links while preserving its uploaded files.
Password resets leave sharing links active. Revocation blocks new downloads;
downloads already in progress can finish. Public tokens are omitted from the
application's request logs.

## Configuration

The SQLite database defaults to `./uploader.db`. Select another location with
`-db /path/to/uploader.db`. The database stores usernames, password hashes,
administrator roles, per-user disk quotas, login sessions, share links, and server settings.

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
batch is removed. Uploads, file changes, and quota edits for the same user are serialized in the
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
username and folder until an administrator explicitly recycles that account.
Recycling preserves the account's identity and files while issuing a new
passphrase; it does not reactivate previous sessions or public links. Files
uploaded by the previous shared-password implementation remain in the upload
root; they are not automatically assigned to an account.

## API

`POST /upload` accepts `multipart/form-data` with one or more parts named `file`.
Add `?dir=Archive/docs` to upload into an existing folder relative to your root;
omitting `dir` uploads to the root.
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

Successful responses include `saved_as` (the file's relative path for a single
individual file, or the destination folder for a batch; `.` means the root) and
`bytes` (the sum of all file sizes). Directory and multiple-file responses also
include `files` (the number of saved files). Responses include `X-Quota-Limit`,
`X-Quota-Used`, and `X-Quota-Remaining` headers (bytes). All parts named `file` are
saved. Paths must be relative, use `/` separators, and contain no empty, `.` or
`..` components, backslashes, Windows drive prefixes, or control characters. File and directory
components may contain at most 255 bytes; full paths at most 4095 bytes. Names
starting with `.upload-` are reserved for private staging and cannot be accessed.
Duplicate or conflicting paths within a request return HTTP 400. Conflicts with
existing files or folders return HTTP 409 without saving any of the batch.

`GET /?dir=Archive/docs` opens the explorer. `GET /files?dir=Archive/docs`
returns its HTML file-list fragment. `GET /download?path=Archive/docs/report.pdf`
downloads a file; a directory path returns a ZIP, and an empty path downloads
the entire user folder. These endpoints require authentication and can only
access the authenticated user's files. Symbolic links are rejected.

`GET /shares?path=Archive/docs/report.pdf` returns `{"url":"/s/<token>"}`
for an existing link or `{"url":""}` when sharing is disabled.
`POST /shares` accepts URL-encoded `action=create` or `action=revoke`, plus
`path`. Both endpoints require authentication and manage only the signed-in
user's links. Session-authenticated POSTs also require `csrf_token`, like other
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
the binary. The browser loads Tailwind CSS and Font Awesome Free from a CDN via
`templates/assets.html`.
That shared template also sets fields, labels, buttons, and links to a consistent
14px font size and 20px line height, including file picker buttons.
`templates/dialog.html` supplies the common dialog markup and event handling for
the explorer and user management pages. `templates/shares.html` supplies the
sharing controls within that dialog. `templates/validation.html` replaces
native validation popups across all forms with shared, styled tooltips that do
not shift the layout, while keeping the browser's constraint validation and
server-side checks.

`main.go` handles startup, `database.go` persists settings and the schema,
`setup.go` handles first-run provisioning, `users.go` manages accounts, and
`passphrase.go` generates credentials using the embedded EFF word list (see
[`wordlists/README.md`](wordlists/README.md) for source and license).
`session.go` and `auth.go` handle authentication. `admin.go`, `index.go`, and
`upload.go` serve user management, the explorer page, and the upload endpoint.
`files.go` lists and manages stored items, `storage.go` validates paths and confines
access to user roots, and `download.go` serves files and ZIP archives.
`upload_files.go` streams and publishes batches. `server.go` handles routing and
shutdown; `quota.go` measures storage and enforces quotas. `upload_progress.go`
tracks uploads and serves authenticated progress snapshots.
Size and filename helpers remain separate.
SIGINT and SIGTERM allow active requests up to 10 seconds to finish.

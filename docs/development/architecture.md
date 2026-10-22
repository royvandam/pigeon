# Project layout and architecture

The repository uses the applicable directory conventions from
[`golang-standards/project-layout`](https://github.com/golang-standards/project-layout):

```text
cmd/pigeon/         Executable entry point and signal handling
internal/pigeon/    Private application code and Go tests
  wordlists/        Embedded passphrase dictionary and attribution
internal/token/     Random tokens for sessions, share links, and forms
pkg/database/       SQLite connection and schema migrations
pkg/quota/          Quota rules, usage measurement, and the usage cache
pkg/storage/        Storage provider interfaces and path rules
  local/            Storage provider for the local filesystem
web/                Embedded templates and browser assets
  static/           JavaScript, shared and generated CSS
    fontawesome/    Vendored Font Awesome Free styles, font, and license
  templates/        Server-rendered HTML
test/browser/       Browser-script regression tests
scripts/            Development tooling helpers
docs/               MkDocs guides, API reference, and audit archive
mkdocs.yml          Documentation site configuration and navigation
```

Go tests stay beside the packages they exercise; additional browser tests live
under `test/`. The `web` package embeds templates and static files at build time,
and the application embeds its passphrase word list. A built executable runs
without the source tree or an asset directory. Packages under `pkg/` can be
used as a library; the rest of the application stays under `internal/`.

## Storage providers

Account files live behind the `storage.Provider` interface in `pkg/storage`.
A provider keeps one namespace per account. An open namespace is a
`storage.Tree` of folders and regular files with slash-separated paths, which
supports listing, walking, opening files and folders, creating folders,
renaming, removal, and staged uploads. File information uses the standard
`io/fs` types. `storage.ValidatePath` holds the path rules every provider
enforces; names starting with `storage.ReservedPrefix` belong to the provider,
and listings and walks leave them out.

`pkg/storage/local` stores each namespace as a folder under the upload root.
Every operation goes through an `os.Root`, which confines it to the account,
and symbolic links and special files are rejected anywhere in a path. Uploads
are staged in a reserved folder of the account and renamed into place when
published, with a rollback if a rename fails. Open files and folders stay bound
to their item when it is moved, so downloads can stream after the account lock
is released. At startup, `RemoveStaleUploads` deletes staging folders left by a
crash. Another backend, such as object storage, implements the same interfaces.

## Quotas and usage

`pkg/quota` holds the quota rules. Every file and folder is charged at least
one allocation unit, 4 KiB by default, and file sizes are rounded up to whole
units. `quota.Measure` walks a storage tree to compute an account's usage, and
`quota.Cache` keeps each account's usage up to date so requests need not walk
it again: callers hold the account lock while they read or change an entry,
and entries are measured again after 10 minutes so changes made directly in
storage still show.

## Request and storage ownership

Account changes are coordinated by `accountManager` in `accounts.go`; the SQLite
store owns persistence and transactions. `userStorage` names an account's
namespace in the storage provider and measures its usage. `accountAccess` locks
an account and rechecks its identity before both reads and writes; download
handles release that lock before streaming. Uploads hold the lock only to check the quota and to publish:
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


`cmd/pigeon/main.go` handles signals and invokes `pigeon.Run`.
`pkg/database` opens SQLite with foreign keys, WAL journaling, and a single
connection, which serializes transactions, and migrates the schema for
accounts, sessions, and share links. Inside `internal/pigeon`, `run.go`
handles startup, `database.go` persists server settings, `setup.go` handles
first-run provisioning, and `users.go` manages accounts. `passphrase.go`
generates credentials using the embedded EFF word list. Its source and license are documented in
[passphrase word list attribution](https://github.com/royvandam/pigeon/blob/main/internal/pigeon/wordlists/README.md).
`session.go` and `auth.go` handle authentication. `admin.go`, `index.go`, and
`upload.go` serve user management, the explorer page, and the upload endpoint.
`files.go` handles explorer requests, and `download.go` serves files and ZIP
archives. `upload_files.go` streams and publishes batches. `server.go` handles
routing and shutdown, and `quota.go` checks and changes account quotas.
`upload_progress.go` tracks uploads and serves authenticated progress
snapshots.
Size and filename helpers remain separate.
SIGINT and SIGTERM allow active requests up to 10 seconds to finish.

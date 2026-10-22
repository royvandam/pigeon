# Project layout and architecture

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
docs/               MkDocs guides, API reference, and audit archive
mkdocs.yml          Documentation site configuration and navigation
```

Go tests stay beside the packages they exercise; additional browser tests live
under `test/`. The `web` package embeds templates and static files at build time,
and the application embeds its passphrase word list. A built executable runs
without the source tree or an asset directory. There is no public Go library;
application code stays under `internal/`.

## Request and storage ownership

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


`cmd/pigeon/main.go` handles signals and invokes `pigeon.Run`.
Inside `internal/pigeon`, `run.go` handles startup, `database.go` persists
settings and the schema, `setup.go` handles first-run provisioning, and
`users.go` manages accounts. `passphrase.go` generates credentials using the
embedded EFF word list. Its source and license are documented in
[passphrase word list attribution](https://github.com/royvandam/pigeon/blob/main/internal/pigeon/wordlists/README.md).
`session.go` and `auth.go` handle authentication. `admin.go`, `index.go`, and
`upload.go` serve user management, the explorer page, and the upload endpoint.
`files.go` handles explorer requests, and `download.go` serves files and ZIP
archives. `upload_files.go` streams and publishes batches. `server.go` handles
routing and shutdown; `quota.go` enforces quotas, and `storage_usage.go` measures
usage. `upload_progress.go` tracks uploads and serves authenticated progress
snapshots.
Size and filename helpers remain separate.
SIGINT and SIGTERM allow active requests up to 10 seconds to finish.

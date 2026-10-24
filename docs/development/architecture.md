# Project layout and architecture

The repository uses the applicable directory conventions from
[`golang-standards/project-layout`](https://github.com/golang-standards/project-layout):

```text
cmd/pigeon/         Entry point, application configuration, and startup
configs/            Example configuration file
internal/size/      Reading and formatting storage sizes
internal/token/     Random tokens for sessions, share links, and forms
pkg/account/        Accounts, passwords, account locks, and lifecycle changes
  wordlists/        Embedded passphrase dictionary and attribution
pkg/auth/           Sign-in sessions and rate-limited password checks
pkg/configuration/  Configuration file loading and value types
pkg/database/       SQLite connection and versioned schema
pkg/files/          Account file operations, downloads, and uploads
pkg/quota/          Quota rules, usage measurement, and the usage cache
pkg/share/          Public download links
pkg/server/         HTTP handlers, pages, and the HTTP server
pkg/storage/        Storage provider interfaces and path rules
  configuration/    Choosing a storage provider from configuration
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
without the source tree or an asset directory. Packages under `pkg/` form a
library that `cmd/pigeon` assembles into the server; `internal/` holds helpers
that are not part of the library's API.

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

## Database

`pkg/database` opens SQLite with foreign keys, WAL journaling, and a single
connection, which serializes transactions, and creates the schema for
accounts, sessions, and share links in a new database. The stores in
`pkg/account`, `pkg/auth`, and `pkg/share` read and change their tables through
the returned `*sql.DB`.

The schema has a version, `database.SchemaVersion`, kept in the database as
`PRAGMA user_version`. Opening a database from a later release, or one without
a version that already has tables, fails rather than guessing at its contents.
A release that changes the schema raises the version and adds a numbered step
that converts databases from the previous one.

## Accounts

`pkg/account` owns accounts. `account.Store` reads and changes them in the
database and takes no locks. Usernames are case-insensitive, and each account
stores its files under a slug made from its username; deleted accounts keep
both reserved until they are purged. Passwords are hashed with bcrypt, and an
unknown username is checked against a dummy hash of the same cost, so it takes
as long as a wrong password. Generated passphrases use the embedded EFF word
list; its source and license are documented in the
[passphrase word list attribution](https://github.com/royvandam/pigeon/blob/main/pkg/account/wordlists/README.md).

`account.Locks` serializes the work on each account:

- `Lock` is the account lock. Every read and change of the account's files
  holds it, as do quota changes, recycling, and purging.
- `LockUploadSlot` lets one upload per account stream at a time.
- `LockForRemoval` takes both, so purging and recycling wait for an upload
  that is still streaming.

`account.Manager` makes the changes an administrator requests: creating,
deleting, recycling, and purging accounts, resetting passwords, and setting
quotas. It takes the locks a change needs and coordinates the database with the
account's files and cached usage. A purge keeps the account reserved until its
files are removed, so a failure leaves it intact for a retry.

## Authentication

`pkg/auth` authenticates requests. `auth.Authenticator` checks passwords for
the sign-in form and HTTP Basic authentication. Each client may make 10 failed
attempts, and one more becomes available every 6 seconds; the limit applies
before bcrypt runs, and successful sign-ins return their attempt. At most one
check per CPU runs at a time. The HTTP layer identifies the client from its
address, or from `X-Forwarded-For` when the request comes from a trusted proxy,
and `auth.ClientKey` groups IPv6 clients by their /64 network.

`auth.Sessions` stores browser sessions as hashes of their tokens, each with a
CSRF token. A session lasts 12 hours and ends when the account is deleted or
its password changes.

## Share links

`pkg/share` stores public download links. Only a hash of each token is kept,
so a leaked database or backup yields no working links. Creating a link for an
item replaces its previous one, and renaming, moving, or deleting an item
revokes the links for it and everything below it before the path changes, so a
reused path never revives an old link. Links of deleted accounts stop working.

## Account files

`pkg/files` reads and changes the files of accounts. `files.Service.Open`
takes the account lock and checks that the identity it acts for is still
current, so a request authenticated before a deletion, password change, or
purge cannot reach the account's files, or those of a new account that reuses
the username. The returned `files.Access` lists, creates folders, renames,
moves, deletes, opens downloads, and creates and removes share links:

- new names are checked for invisible formatting characters;
- a share link needs an existing item other than the top folder, but can be
  removed after its item is gone;
- a new folder is charged one allocation unit;
- renames, moves, and deletions revoke the share links of the item and
  everything below it, after validation and before the change;
- each change keeps the cached usage up to date, or drops it when a change may
  have partly failed.

A `files.Download` holds its own handle to the file or folder, so it streams
after the account lock is released; folders are sent as ZIP archives.
`files.Service.OpenShared` opens a share link's item under the account lock
and rechecks the link after waiting for it.

An upload is a `files.Upload`. `BeginUpload` takes the account's upload slot,
which keeps uploads to one account one at a time. `Prepare` takes the account
lock to check the identity and quota and starts staging files, `Add` stages
each file while the lock is released, and `Publish` takes the lock again to
recheck the account and quota before publishing the batch. The account's
pages, share links, and file actions therefore never wait for an upload's
body. A `files.Progress` follows the upload's phases and written bytes.

## HTTP server

`pkg/server` serves the web interface and HTTP API. `server.New` takes the
server's settings as a `server.Configuration`, validates them, and resolves
defaults once, and takes the services it uses as `server.Dependencies`. The
resulting `server.Server` holds only what its handlers need; `Handler` routes
requests through the security-header and logging middleware, and `Run` serves
until its context ends. Handlers are methods of the server. They parse
requests, call the packages above, and render responses, using shared method,
authentication, and error helpers:

- `auth.go` handles sign-in, sign-out, and request authentication, and
  `client.go` identifies clients behind trusted proxies;
- `index.go` and `explorer.go` render the explorer, and `files.go` handles
  file actions;
- `download.go` serves files and ZIP archives, and `shares.go` manages and
  serves share links;
- `upload.go` streams uploads, and `upload_progress.go` tracks their progress
  for the browser and serves authenticated snapshots;
- `admin.go` serves user management, and `accounts.go` turns its actions into
  account changes;
- `server.go` handles routing and shutdown, `deadline.go` limits slow request
  bodies, and `quota.go` formats quotas for forms and headers.

Template loading is in `templates.go`; templates reuse CSRF and sign-out
controls. Browser code and common styles live in `web/static/` and are
embedded into the binary along with the templates. `ActionDialog` uses one
definition per action and one controller for all dialogs. JavaScript tests run
the same files served by the application. Rebuild and restart after changing
templates or assets.

## Configuration

Configuration is declarative and hierarchical, in the style of
[Buildbarn](https://github.com/buildbarn): each package that builds something
from settings defines a configuration type for it, and the application
composes them into one type that mirrors the configuration file:

```go
type ApplicationConfiguration struct {
	Logging  LoggingConfiguration               `json:"logging"`
	Database database.Configuration             `json:"database"`
	Storage  storageconfiguration.Configuration `json:"storage"`
	HTTP     server.Configuration               `json:"http"`
}
```

Configuration types are plain data with JSON field names. Zero values select
defaults, which the package resolves once when it builds from them, as
`server.New` does. A choice between implementations is a struct with one
pointer field per implementation, of which exactly one is set; its factory,
such as `storageconfiguration.NewProviderFromConfiguration`, builds the chosen
one. Each implementation defines its own configuration type and factory, such
as `local.Configuration` and `local.NewProviderFromConfiguration`, and the
choice delegates to them. The choice lives in its own package,
`pkg/storage/configuration`, so that implementations of the storage interfaces
can be chosen without import cycles; a new provider adds a field there.
`pkg/configuration` reads the file strictly, rejecting unknown fields, and
provides value types such as `configuration.Duration`.

## Startup

`cmd/pigeon/main.go` is the composition root. It reads the configuration file
named on the command line and checks every section before it opens the
database. First-run setup in `setup.go` creates the initial administrator.
Startup then removes uploads left by a crash, builds the stores and services,
connects them, and runs the server until SIGINT or SIGTERM; active requests
then get up to 10 seconds to finish.

# Backups, restores, and upgrades

## Run one server per upload root

Account locks and upload queues live in one server process. Run one Pigeon
process for each upload root; several processes sharing the same folders are
not coordinated. Use a dedicated operating-system account with read and write
access to the database directory and the upload root.

For a service, keep the configuration file in a fixed place, such as
`/etc/pigeon/pigeon.json`, and use absolute paths in it. A relative database
path resolved from another working directory can create an empty database and
trigger first-run setup again. See [configuration](configuration.md) before
relocating data.

Pigeon logs to standard error; [`logging.level`](configuration.md#logging)
selects how much. `SIGINT` and `SIGTERM` allow active requests up to 10
seconds to finish before shutdown. Give a service manager more than 10 seconds
to stop it.

## What to back up

Keep a matching copy of both:

| Data | Contents |
| --- | --- |
| SQLite database, at `database.path` | Accounts, password hashes, roles, quotas, sessions, share token hashes, and deleted account records |
| Upload root, at `storage.local.directory` | Each account's files and folders |

Also keep the configuration file, your executable or its source revision, and
the reverse proxy configuration. Treat backups as private: they contain
uploaded files and account information. A restored database restores its saved
account and link state, including links or accounts revoked after the backup
was taken.

## Take a consistent backup

Stop Pigeon before copying the database and uploads together. This prevents an
upload or account change from making the two copies disagree. Pigeon uses
SQLite WAL mode: while the database is open, committed data may still be in
`pigeon.db-wal`. Copying only a live `pigeon.db` is not a complete backup.

For example, after stopping the process, with data under `/srv/pigeon`:

```sh
umask 077
mkdir -p /srv/backups/pigeon-2026-10-22
sqlite3 /srv/pigeon/pigeon.db ".backup '/srv/backups/pigeon-2026-10-22/pigeon.db'"
cp -a /srv/pigeon/uploads /srv/backups/pigeon-2026-10-22/uploads
```

Choose a new backup directory for each snapshot. The SQLite `.backup` command
produces a standalone database snapshot, including committed WAL contents.
Keep Pigeon stopped until the file copy finishes, then restart it. SQLite is
provided by `nix-shell` or can be installed separately.

## Restore

1. Stop Pigeon and keep a copy of the current data before replacing anything.
2. Restore the database and upload root from the same snapshot into a clean
   data directory. Do not mix the restored database with old `-wal` or `-shm`
   sidecars from another database.
3. Give the service account access to the restored files and directories.
4. If the database or upload root moved, update `database.path` and
   `storage.local.directory` in the configuration file. Start Pigeon with it.
5. Check sign-in, a private download, an upload, and the administrator page.

Test restores periodically in an isolated instance. Keep that instance bound
to loopback and give it its own upload root so it cannot change production
files. An old backup can reinstate a share token hash whose URL someone still
holds; revoke or replace links where needed after a restore.

## Upgrade

1. Back up the database and upload root together.
2. Build the desired revision with the Go version declared in its `go.mod`.
3. Stop the old process and replace its executable.
4. Start with the same configuration file and verify the server and file
   operations.

The database records the version of its schema. A release that changes the
schema converts the database when it starts, and a release refuses a database
from a later one. Retain the pre-upgrade backup: a rollback should restore the
matching database and upload snapshot as well as the old executable.

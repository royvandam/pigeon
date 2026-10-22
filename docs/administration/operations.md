# Backups, restores, and upgrades

## Run one server per upload root

Account locks and upload queues live in one server process. Run one Pigeon
process for each upload root; several processes sharing the same folders are
not coordinated. Use a dedicated operating-system account with read and write
access to the database directory and the upload root.

For a service, use an absolute `-db` path and a fixed working directory. A
different relative database path can create an empty database and trigger
first-run setup again. Saved settings include an absolute upload directory.
See [configuration](configuration.md) before relocating data.

Pigeon logs its listen address, upload directory, and requests to standard
error. `SIGINT` and `SIGTERM` allow active requests up to 10 seconds to finish
before shutdown. Give a service manager more than 10 seconds to stop it.

## What to back up

Keep a matching copy of both:

| Data | Contents |
| --- | --- |
| SQLite database, selected with `-db` | Accounts, password hashes, roles, quotas, sessions, share token hashes, server settings, and deleted account records |
| Upload root, saved through `-dir` | Each account's files and folders, plus any legacy shared-password uploads |

Also keep your executable or its source revision and reverse proxy
configuration. Treat backups as private: they contain uploaded files and
account information. A restored database restores its saved account and link
state, including links or accounts revoked after the backup was taken.

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
4. Start Pigeon with the restored `-db` path. If the upload root moved, supply
   `-dir /new/path/uploads` so the saved absolute path is updated.
5. Check sign-in, a private download, an upload, and the administrator page.

Test restores periodically in an isolated instance. Keep that instance bound
to loopback and give it its own upload root so it cannot change production
files. An old backup can reinstate a share token hash whose URL someone still
holds; revoke or replace links where needed after a restore.

## Upgrade

1. Back up the database and upload root together.
2. Build the desired revision with the Go version declared in its `go.mod`.
3. Stop the old process and replace its executable.
4. Start with the same database path and verify the server and file operations.

Database migrations run automatically at startup. Retain the pre-upgrade
backup: an older binary may not understand a migrated database. A rollback
should restore the matching database and upload snapshot as well as the old
executable.

## Older installations

- Existing accounts received the default 1 GiB quota when per-user quotas
  replaced the global upload limit. The `-max` flag was removed.
- The shared `-password` flag and `X-Upload-Password` header were removed; use
  an account and [HTTP Basic authentication](../reference/api.md#authentication)
  for scripts.
- Files from the shared-password implementation remain at the upload root and
  are not assigned to an account automatically.
- Existing timestamped filenames remain accessible; new uploads retain their
  original names.
- Saved listen addresses are preserved, including old externally reachable
  defaults. Review the listener and [HTTPS configuration](deployment.md).
- Earlier plaintext share tokens are converted to hashes on startup without
  changing working links.

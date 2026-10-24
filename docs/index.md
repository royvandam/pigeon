# Pigeon

<img src="assets/logo.webp" width="120" height="120" alt="Pigeon logo" />

Pigeon moves files between devices through a browser or an HTTP client. It is a
self-hosted Go server with individual accounts, per-user storage and disk
quotas, an administrator interface, and shareable download and upload links.

The server streams uploads to disk and embeds its web interface in the
executable. It reads its settings from a JSON configuration file and keeps
accounts in SQLite, so running it requires one binary, a configuration file, a
database, and an upload directory.

## Start here

| You want to… | Read |
| --- | --- |
| Run Pigeon for the first time | [Installation and first run](getting-started.md) |
| Upload, organize, or download files | [Files and uploads](guide/files.md) |
| Send someone a download link, or collect files from them | [Share links](guide/sharing.md) |
| Manage users and storage | [Accounts](administration/accounts.md) and [disk quotas](administration/quotas.md) |
| Make Pigeon available over HTTPS | [HTTPS and reverse proxies](administration/deployment.md) |
| Back up, restore, or upgrade a server | [Operations](administration/operations.md) |
| Automate file transfers | [HTTP API](reference/api.md) |
| Change the application or its docs | [Development](development/contributing.md) and [documentation workflow](development/documentation.md) |

## How storage and sharing work

Every account has a separate folder under the upload root. Users manage their
own files, while administrators create accounts, set quotas, and reset
passwords. Deleting an account disables access and keeps its files until an
administrator recycles or purges it.

Uploads preserve filenames and directory structure. A batch is published only
after the entire request succeeds; existing files are never overwritten.
Folder downloads are ZIP archives. Anyone holding a download link can download
its file or folder without signing in, and anyone holding a folder's upload
link can add files to it without seeing what it holds, until the owner replaces
or revokes the link or it expires.

## Documentation and review history

These guides describe the current implementation. The [audit archive](audit/README.md)
records historical reviews, findings, and their resolution; its findings are
not a current feature reference.

The [source repository](https://github.com/royvandam/pigeon) contains the Go
application, embedded assets, tests, and this documentation. The MkDocs site
is configured for <https://royvandam.github.io/pigeon/>; see the
[publishing instructions](development/documentation.md#publish-with-github-pages)
to enable hosting.

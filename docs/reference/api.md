# HTTP API

Pigeon exposes the same routes used by its web interface. Examples below use
the local loopback server; use your HTTPS origin for a remote server. Paths
are relative to the authenticated user's storage root.

| Method | Route | Result |
| --- | --- | --- |
| `GET` | `/` | Explorer page; optional `dir`. |
| `GET` | `/files` | HTML file-list fragment; optional `dir`. |
| `POST` | `/files` | Create, rename, move, or delete an item. |
| `POST` | `/upload` | Stream one or more multipart files; optional `dir`. |
| `GET` | `/upload-progress` | JSON progress for an upload `id`. |
| `GET`, `HEAD` | `/download` | Private file or ZIP download; optional `path`. |
| `GET`, `POST` | `/shares` | Inspect, create, replace, or revoke a share link. |
| `GET`, `HEAD` | `/s/<token>` | Public file or ZIP download; no account required. |
| `GET`, `POST` | `/u/<token>` | Public upload page and multipart upload into a folder; no account required. |
| `GET` | `/u/<token>/progress` | JSON progress for an upload `id` through that link. |

## Authentication

Authenticate using HTTP Basic authentication with a username and password:

```sh
curl --user 'Jane Doe' --form 'file=@document.pdf' http://127.0.0.1:8080/upload
```

Curl prompts for the password and sends it with the first request. Requests
without valid credentials receive HTTP 401 with a Basic challenge, unless they
identify as coming from a browser through Fetch Metadata headers,
`Upgrade-Insecure-Requests`, or `X-Requested-With: XMLHttpRequest`. A browser
would answer the challenge with its own sign-in prompt and then send that
password with every request, bypassing the CSRF check and Sign out. Browsers
instead get a plain 401, and page loads are redirected to the sign-in page.

Session-authenticated POST requests need `csrf_token` in URL-encoded forms,
or `X-CSRF-Token` for uploads. Basic-authenticated clients do not need a CSRF
token. Both kinds of POST requests must pass the origin check if they send an
`Origin` header. See [security](../administration/security.md) for sessions
and [deployment](../administration/deployment.md) for HTTPS and throttling.

The examples let curl prompt for the password rather than placing it in the
command line. Add `--fail-with-body` in scripts to return a nonzero exit code
on HTTP errors while retaining the server's error message.

## Upload files

`POST /upload` accepts `multipart/form-data` with one or more parts named `file`.
Add `?dir=Archive/docs` to upload into an existing folder relative to your root;
omitting `dir` uploads to the root. Other part names are ignored; at least one
`file` part is required.

To preserve a directory hierarchy, set each part's
filename to its slash-separated relative path:

```sh
curl --user 'Jane Doe' \
  --form 'file=@project/readme.txt;filename=project/readme.txt' \
  --form 'file=@project/docs/guide.pdf;filename=project/docs/guide.pdf' \
  http://127.0.0.1:8080/upload
```

Successful responses include `saved_as` (the file's relative path for a single
individual file, or the destination folder for a batch; `.` means the root) and
`bytes` (the sum of all file sizes). Directory and multiple-file responses also
include `files` (the number of saved files). Responses include `X-Quota-Limit`,
`X-Quota-Used`, and `X-Quota-Remaining` headers (bytes).

A successful single-file upload returns HTTP 200 with a **plain-text** body:

```text
ok
saved_as=document.pdf
bytes=1234
```

A directory or multiple-file upload adds a line such as `files=2`. The `bytes`
value counts actual file bytes, while quota headers include the
[allocation units](../administration/quotas.md) charged for files and folders.

### Path rules and request limits

All parts named `file` are saved. Paths must be relative, use `/` separators,
and contain no empty, `.` or `..` components, backslashes, Windows drive
prefixes, or control characters. File and directory components may contain at
most 255 bytes; full paths at most 4095 bytes. Names
starting with `.upload-` are reserved for private staging and cannot be accessed.
Uploaded paths, new folders, and renames also cannot contain invisible
formatting characters (Unicode category Cf), such as right-to-left overrides
that make `invoice`, U+202E, `fdp.exe` display as `invoiceexe.pdf`. Zero-width joiners and
non-joiners are allowed, because some scripts and emoji need them. Existing items
with such names, such as files copied into the upload root, remain accessible,
and downloads suggest their names without these characters.
Duplicate or conflicting paths within a request return HTTP 400. Conflicts with
existing files or folders return HTTP 409 without saving any of the batch.

An upload fails with HTTP 408 when no data arrives for 60 seconds; form
submissions, including sign-in, must arrive within 30 seconds. An account's
uploads are received one at a time, but its pages, share links, and file actions
do not wait for them. The quota, destination folder, and account are checked
again before an upload is published, so changes made meanwhile are respected.

## Browse and download

`GET /?dir=Archive/docs` opens the explorer. `GET /files?dir=Archive/docs`
returns its HTML file-list fragment. `GET /download?path=Archive/docs/report.pdf`
downloads a file; a directory path returns a ZIP, and an empty path downloads
the entire user folder. These endpoints require authentication and can only
access the authenticated user's files. Symbolic links are rejected.

`HEAD /download` returns download headers without the body. File downloads
support byte ranges; directory ZIPs are generated as they stream.

Use query encoding when paths contain spaces or other special characters:

```sh
curl --fail-with-body --user 'Jane Doe' --get \
  --data-urlencode 'path=Archive/docs/report.pdf' \
  --output report.pdf http://127.0.0.1:8080/download
```

## Manage share links

An item can have one link of each kind: `kind=download`, the default, for a
file or folder, and `kind=upload` for a folder.

`GET /shares?path=Archive/docs/report.pdf&kind=download` returns
`{"url":"","active":true,"kind":"download","expires_at":null}` when the item
has a link of that kind, or `"active":false` when it does not; the URL of an
existing link is not stored and cannot be returned. `expires_at` is when an
active link stops working, in RFC 3339 UTC, or `null` if it does not expire.

`POST /shares` accepts URL-encoded `action=create` or `action=revoke`, plus
`path` and `kind`. `create` also accepts `lifetime`, a duration such as `1h`,
`24h`, or `720h`, up to a year; without it, the link does not expire. It
returns the new link, such as `{"url":"/s/<token>","active":true,...}`,
replacing any existing link of that kind for that item. Both endpoints require
authentication and manage only the signed-in user's links.
Session-authenticated POSTs also require `csrf_token`, like other file
operations. The path must name a file or folder; sharing the account root is
not supported. An unknown kind, an invalid lifetime, or an upload link for a
file returns HTTP 400.

For example, create a link to an existing file:

```sh
curl --fail-with-body --user 'Jane Doe' \
  --data-urlencode 'action=create' \
  --data-urlencode 'path=Archive/docs/report.pdf' \
  http://127.0.0.1:8080/shares
```

The JSON response's `url` is a path such as `/s/<token>`; prepend the server's
public origin to share it. Save that URL immediately. Repeating `create`
replaces the link and invalidates the old token. Send `action=revoke` with
the same `path` and `kind` to disable sharing; the response has an empty `url`
and `active: false`.

## Use share links

`GET /s/<token>` and `HEAD /s/<token>` provide the shared download without
authentication. File downloads support byte ranges. Query parameters cannot
change the shared item. Invalid, revoked, or expired links, and upload links,
return HTTP 404; these URLs allow downloads only.

`GET /u/<token>` serves the upload page of an upload link. `POST /u/<token>`
takes the same multipart form as `/upload`, without authentication, and saves
the files in the link's folder. Names are relative to that folder; a taken
first path element is replaced by the next free name, such as
`report (2).pdf` or `Photos (2)`. The response is the same as for `/upload`,
with `saved_as` relative to the link's folder, but it sends no `X-Quota-*`
headers, and a quota error does not say how much space is left. An optional
`X-Upload-ID` header enables `GET /u/<token>/progress?id=<id>`. For example,
create a link that works for a week and upload through it:

```sh
curl --fail-with-body --user 'Jane Doe' \
  --data-urlencode 'action=create' --data-urlencode 'path=Inbox' \
  --data-urlencode 'kind=upload' --data-urlencode 'lifetime=168h' \
  http://127.0.0.1:8080/shares
curl --fail-with-body --form 'file=@scan.pdf' \
  http://127.0.0.1:8080/u/<token>
```

Invalid, revoked, or expired links, and download links, return HTTP 404. An
upload also fails with 404 when its link is revoked, replaced, or expires, or
its folder is renamed, moved, or deleted, before the files are saved.
Cross-site browser posts return HTTP 403.

## Change files and folders

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
curl --user 'Jane Doe' --data-urlencode 'action=mkdir' \
  --data-urlencode 'name=Archive' http://127.0.0.1:8080/files
curl --user 'Jane Doe' --form 'file=@document.pdf' \
  'http://127.0.0.1:8080/upload?dir=Archive'
```

## Track upload progress

To track progress, send a unique `X-Upload-ID` header (16–64 ASCII letters, digits,
underscores, or hyphens) with the upload, then poll
`GET /upload-progress?id=<upload-id>` with the same account's authentication.
The JSON response has `bytes_received` (file bytes written so far) and `state`
(`queued`, `preparing`, `uploading`, `finalizing`, `complete`, or `failed`).
Progress records are held in memory and expire five minutes after completion or
failure; other users cannot read them. Reusing an existing ID returns HTTP 409.
An account, or an upload link, can have 16 tracked uploads in progress at a
time; another tracked upload returns HTTP 429 until one finishes. The header is
optional for clients that do not need progress.

For example, start an upload with a fresh ID:

```sh
curl --fail-with-body --user 'Jane Doe' \
  --header 'X-Upload-ID: example-upload-0001' \
  --form 'file=@document.pdf' http://127.0.0.1:8080/upload
```

While it runs, poll from another terminal with the same account:

```sh
curl --fail-with-body --user 'Jane Doe' --get \
  --data-urlencode 'id=example-upload-0001' \
  http://127.0.0.1:8080/upload-progress
```

An example snapshot is:

```json
{"state":"uploading","bytes_received":1048576}
```

| State | Meaning |
| --- | --- |
| `queued` | Waiting for the account's upload slot. |
| `preparing` | Checking account storage and preparing the temporary batch. |
| `uploading` | Reading and writing file bytes. |
| `finalizing` | Rechecking quota and destination, then publishing the batch. |
| `complete` | The batch was saved successfully. |
| `failed` | The request failed; temporary files are removed. |

Polls before the upload registers, after the five-minute retention period, or
after a server restart return HTTP 404. Do not treat the byte count alone as
success; wait for `complete` and check the upload response.

## Errors and status codes

Errors generally have a plain-text body. Clients should use the HTTP status
code to decide what to do, rather than depending on exact error wording.

| Status | Typical cause | Client action |
| --- | --- | --- |
| `303` | Successful file change or browser navigation redirect. | Follow the redirect when displaying the explorer. |
| `400` | Invalid path, multipart form, action, or upload ID. | Correct the request. |
| `401` | Missing or invalid credentials, or an account invalidated during upload. | Check the account and credentials. |
| `403` | Invalid CSRF token, rejected origin, or administrator access required. | Check session, origin, and permissions. |
| `404` | Missing item, unavailable progress record, or invalid/revoked share. | Check the path or request a current link. |
| `405` | Unsupported method. | Use a method from the `Allow` header. |
| `408` | Stalled request body or canceled operation. | Check the connection and retry when ready. |
| `409` | Existing destination, conflicting paths, or reused upload ID. | Resolve the conflict or choose a new ID. |
| `413` | Account quota or request-size limit exceeded. | Free storage, raise quota, or reduce the request. |
| `429` | Too many failed authentication attempts, or tracked uploads in progress. | Wait for the `Retry-After` interval, or for an upload to finish. |
| `500` | Database or filesystem failure. | Check server logs and storage permissions. |

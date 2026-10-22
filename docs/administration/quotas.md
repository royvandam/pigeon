# Disk quotas

Every account, including the initial administrator, defaults to `1GB` of total
stored files (1 GiB, or 1,073,741,824 bytes). Admins can supply another quota when
creating a user and edit existing users' quotas on `/admin`. Quotas must be
positive; values such as `500MB`, `2GB`, or `100B` use powers of 1024.

## How usage is counted

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
quota with the same rounding before sending it.

## Concurrent changes and quota reductions

Quota checks, publication, file changes, and quota edits for the same user are
coordinated by account locks. Each account receives one upload at a time, while
its other file actions can continue as the request body streams. The server
checks quota again before publishing the batch; different users can upload
simultaneously. Run one server process per upload root.

A quota may be reduced below current usage: existing files are
preserved and new uploads are blocked until usage falls below the quota or an
admin raises it.

For changes from older quota settings, see [older installations](operations.md#older-installations).

# Share links

Choose **Share** beside a file or folder to manage its links. A file can have a
download link. A folder can have a download link and an upload link, each with
its own URL and lifetime. Opening the Share dialog does not create anything
until you choose **Create link**.

## Download links

Anyone with a download link can download the item without an account. Files
download as attachments; folders download as ZIPs containing their current
contents, including files added after the link was created.

## Upload links

Anyone with a folder's upload link can upload files and folders into it
without an account, from a page that shows only the folder's name and an
upload form. They cannot see, download, or change what the folder already
holds, or anything uploaded by others.

- **Taken names are renamed.** An upload whose name is taken in the folder is
  saved under the next free name, such as `report (2).pdf` or `Photos (2)`. An
  uploaded folder whose name is taken becomes a new folder, so nothing is added
  to an existing one, and nothing is ever replaced. If you create an item with
  the new name while the upload is still running, the upload fails instead;
  trying again picks another name.
- **Uploads count against your quota.** People with the link can fill your
  storage. Use a lifetime, and revoke the link when you have what you need.
- **Uploads share your upload queue.** Uploads to one account run one at a
  time, including your own, so a long upload through the link makes yours wait.
- **The link must still work when an upload finishes.** If you revoke or
  replace it, it expires, or the folder is renamed, moved, or deleted while
  files are uploading, the upload is discarded.

## Lifetimes

Before choosing **Create link**, pick how long the link works: 1 hour, 1 day,
7 days, 30 days, or until revoked. Upload links default to 7 days and download
links to no expiry. The dialog shows when an active link expires. An expired
link stops working at once; create a new one to share the item again.

## Copy, replace, or revoke a link

Only a SHA-256 hash of each link's token is stored, like session tokens, so a
leaked database or backup does not reveal working links. The URL is therefore
shown only once, right after **Create link**; copy it then. If clipboard
access is unavailable, the dialog selects the URL so you can copy it manually.

Opening **Share** again shows that a link is active, and when it expires.
**Replace link** creates a new URL with the lifetime you pick and disables the
old one; **Revoke link** disables it. Links survive restarts.

## Changes that affect access

Renaming, moving, or deleting an item revokes the links to that item and its
descendants, so recreating an old path cannot expose replacement files.
Deleting an account also revokes all its links while preserving its uploaded
files. Password resets leave links active. Revocation and expiry block new
downloads, and uploads that have not finished; downloads already in progress
can finish. Public tokens are omitted from the application's request logs.

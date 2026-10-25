# Files and uploads

## Browse and organize

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

## Upload files or directories

Drop files and folders anywhere on the explorer page to upload them right away,
with all files in their subfolders. Or use **Choose files** to select one or
several files, or **Choose folder** to select a folder and all files in its
subfolders, and then **Upload here**; browsers pick either files or one folder
at a time. A drop can mix files and folders; folders without files are skipped.
While an upload runs, new drops are refused until it finishes. The page shows
the number of selected files and their combined size, and once an upload
starts, its progress.

Uploads go into the folder currently open in the explorer. Original filenames
(including spaces, Unicode, and dotfiles) and directory hierarchies are preserved,
without timestamped filenames or wrapper folders. For example, uploading
`project/docs/readme.txt` while browsing `Archive` saves it as
`uploads/jane-doe/Archive/project/docs/readme.txt`.
Directory uploads can add files to existing folders. Existing files are never
replaced: a filename conflict rejects the whole upload with HTTP 409. Rename,
move, or delete the existing item before uploading a replacement. Previously
uploaded timestamped files and folders remain accessible in the explorer.
The directory picker includes files; empty directories are not created.

The combined file size must fit the user's remaining quota. Each batch is
streamed to a temporary directory and published only after the entire request
succeeds. Invalid paths, duplicate paths, interrupted requests, or quota failures
remove the whole temporary batch and preserve earlier uploads.

## Understand progress

Upload progress follows file bytes processed by the server, including when a
proxy buffers the browser's request. The bar reaches 100% only after the server
confirms a successful upload. Speed and the approximate time remaining use a
rolling five-second window of server progress, excluding multipart overhead.
Queued uploads and storage checks show their current phase; final saving shows
**Finishing…** until confirmed. A stalled transfer stops reporting a countdown.

For request errors and stalled transfers, see [troubleshooting](../troubleshooting.md).
For scripts and command-line uploads, see the [HTTP API](../reference/api.md).

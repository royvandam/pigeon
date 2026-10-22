# Troubleshooting

## The server asks for an administrator again

Check the `-db` path and working directory. The default `./pigeon.db` can select
a new database if you start from a different directory. Stop the process and
restart it with the intended absolute database path. Do not complete setup in
the empty database if you intended to use an existing installation.

## Another device cannot connect

The default `127.0.0.1:8080` listener is local to the server. Expose Pigeon
through an [HTTPS reverse proxy](administration/deployment.md) and use the
proxy's public address. Check that the proxy can reach the listener and that
the hostname and TLS certificate are configured correctly.

## Sign-in fails or returns HTTP 429

Ask an administrator to reset a forgotten password. Generated passphrases are
shown only once; they cannot be retrieved later. A reset signs the user out
on all devices.

After repeated failed attempts, wait for the `Retry-After` interval before
trying again. If many users behind a proxy share the same limit, set
`-trusted-proxies` to the proxy's addresses so Pigeon can identify clients.
See [sign-in throttling](administration/deployment.md#sign-in-throttling-and-reverse-proxies).

## A form or upload returns HTTP 403

Reload the page and sign in again if the session expired. Browser form
requests require a CSRF token. Behind a reverse proxy, check the public
`Host` header and `-public-url`; an incorrect origin is also rejected.
If `-public-url` is HTTPS, use HTTPS in the browser so it can send Secure
cookies.

## An upload returns HTTP 409

An existing file or a file/folder path conflict prevents the entire batch from
being saved. Pigeon does not overwrite files. Rename, move, or remove the
conflicting item, or upload into another folder. API clients should also use a
new `X-Upload-ID` for every tracked upload.

## An upload returns HTTP 413 despite enough apparent space

Check the account's remaining [quota](administration/quotas.md). Files are
rounded up to 4 KiB units, and each folder also uses 4 KiB. A large collection
of tiny or empty files can use more quota than its combined byte size suggests.
Deleting files or raising the account quota makes space available.

A reverse proxy can also return 413 before Pigeon sees the upload. Remove or
raise the proxy's body limit; the nginx example in the
[deployment guide](administration/deployment.md#serving-over-https) disables it.

## An upload stalls or returns HTTP 408

Pigeon stops reading an upload after 60 seconds without incoming data.
Ordinary form bodies must arrive within 30 seconds. Check network interruptions
and proxy timeouts. Stream request bodies through the proxy rather than
buffering the complete upload first.

Progress counts file bytes processed by the server, so buffering can delay the
bar even if the browser has sent data. **Finishing…** means the server has not
yet confirmed that the batch is saved. Failed requests remove their temporary
batch and leave earlier uploads intact.

## A shared link returns HTTP 404

The link may have been revoked or replaced, its item renamed, moved, or
deleted, or its account deleted. Ask the owner for a new link. Opening the
Share dialog cannot recover a previously generated URL; **Replace link**
creates a new one and disables the old URL.

## A username is unavailable after deletion

Deleted accounts reserve their username and normalized storage folder. An
administrator can recycle the account to retain its files, or purge it to
remove the files and release the name. A failed purge can already have removed
some files; correct the filesystem error and retry. See
[account lifecycle](administration/accounts.md#storage-and-deleted-accounts).

## Templates or styles did not change after an edit

Rebuild and restart the executable: web assets are embedded at build time.
After changing Tailwind classes, run `go generate ./web` and commit the
generated stylesheet. See [development](development/contributing.md).

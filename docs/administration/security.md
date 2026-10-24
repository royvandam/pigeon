# Security

## Accounts and sessions

The browser uses an HttpOnly session cookie valid for 12 hours. Forms require
a CSRF token, and browser uploads send it in the `X-CSRF-Token` header. Session
tokens are stored as hashes. Passwords use bcrypt. Serve the application over
HTTPS whenever other machines can reach it; see
[HTTPS deployment](deployment.md#serving-over-https).

Administrators see generated passphrases only when creating or resetting an
account. A password reset invalidates sessions but leaves public share links
active. Deleting an account invalidates its sessions and shares immediately;
recycling does not restore them. See [accounts](accounts.md).

## Public links

Anyone with a download link can download its item without an account; folder
links expose the folder's current contents, including files added later.
Anyone with an upload link can add files to its folder without an account.

- **Upload links write to the owner's storage.** Uploads through a link are
  charged to the owner's quota and wait in the owner's upload queue, so anyone
  with the link can fill the quota or keep the owner's uploads waiting. Give
  upload links a lifetime and revoke them when they are no longer needed.
- **Upload links reveal little.** The upload page shows the folder's name
  only. It never lists, downloads, or replaces existing items: a taken name is
  saved under a new one, and responses do not report the owner's quota or
  usage. An upload link's token does not work as a download link.
- **Links can expire.** The owner picks a lifetime when creating a link, from
  1 hour to 30 days, or none. Download links default to none, so revoke or
  replace them when access should end.
- **Tokens are protected.** Only token hashes are stored in SQLite, and
  application request logs redact the tokens of `/s/` and `/u/` URLs. A
  reverse proxy has its own logs; configure it to protect share URLs as well.

See [share links](../guide/sharing.md).

## Filesystem and browser boundaries

Requests access files inside the authenticated user's storage root. Paths are
validated, and symbolic links and special files cannot be downloaded. Keep
operating-system access to the database and upload root limited to the service
and administrators. An application's account boundary does not replace host
filesystem permissions or private [backups](operations.md).

Every response sends a Content Security Policy that allows only same-origin
scripts, styles, fonts, and requests, and forbids inline code, plugins, base-URL
changes, form posts to other sites, and framing. Responses also send
`X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, and
`Referrer-Policy: same-origin`; public share links use `no-referrer`. Templates
must not contain inline scripts, `style` attributes, or event handler
attributes. A test enforces this.

## Review history

See the [audit archive](../audit/README.md) for dated security and code-quality
reviews and recorded resolutions. Those documents describe the revisions they
reviewed. Development checks and dependency scanning are documented in
[development](../development/contributing.md#checks-and-builds).

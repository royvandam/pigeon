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

Anyone with a share URL can download the item without an account. Links have
no automatic expiry; revoke or replace them when access should end. Folder
links expose the folder's current contents, including files added later.
Only token hashes are stored in SQLite, and application request logs redact
public share tokens. A reverse proxy has its own logs; configure it to protect
share URLs as well. See [sharing](../guide/sharing.md).

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

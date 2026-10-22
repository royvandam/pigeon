# Accounts

## Create users

Administrators can open `/admin` to create
regular users or additional administrators, assign and change disk quotas, delete
users, and reset passwords. Creating a user or resetting a password generates
a random four-word passphrase, separated by hyphens, and shows it on the
result page. Copy it before leaving that page; it cannot be retrieved
later. Only its bcrypt hash is stored. Share the generated passphrase with the
intended user through a private channel. Set a quota when creating the account,
or change it later from the same page; see [disk quotas](quotas.md).

## Delete, recycle, or purge

User deletion uses the same confirmation dialog as file deletion and preserves
the user's uploaded files.
Deleted accounts appear under **Deleted users**. **Recycle account** asks for
confirmation, restores the account's role, quota, and uploaded files, and
displays a fresh four-word passphrase. Old passwords, sessions, and share links
remain invalid. Creating a user with a deleted account's username or normalized
folder name offers this recycling action instead of just reporting a conflict.
**Purge account** permanently removes a deleted account and all its uploaded
files and folders after confirmation. Its username and normalized folder name
can then be used for a new account with empty storage. Purging is available only
for deleted accounts; active users must be deleted first.
Regular users can manage their own files but cannot access user management. An
administrator cannot delete their own account or the last administrator.

| Action | Access | Files | Old passwords, sessions, and links |
| --- | --- | --- | --- |
| Delete | Disabled immediately | Preserved; username stays reserved | Invalidated |
| Recycle | Restored with a new passphrase | Preserved | Remain invalid |
| Purge | Account removed permanently | Permanently deleted; username released | Remain invalid |

## Reset a password

**Reset password**
opens a confirmation dialog; Cancel or Escape leaves the password and sessions
unchanged. Confirming the reset signs that user out on every device. Resetting
your own password shows the new passphrase on a signed-out result page with a
link to sign in again. Public share links remain active after a password reset;
revoke them separately if needed.

## Storage and deleted accounts

An upload from `Jane Doe` is saved in `uploads/jane-doe/`. Names are
normalized with Unicode case folding, accents are removed where possible, and
punctuation and whitespace become hyphens. Other Unicode letters and digits are
retained. Username comparisons are case-insensitive. Creation fails if the
username or normalized folder name is already reserved.

Deleting an account disables access immediately and invalidates its sessions and
share links.
Its files remain on disk, and a small deleted account record reserves its
username and folder until an administrator explicitly recycles or purges that
account.
Recycling preserves the account's identity and files while issuing a new
passphrase; it does not reactivate previous sessions or public links. Purging
permanently deletes the account and its uploads, including empty folders and
unfinished upload files, and releases its username and normalized folder name.
If file removal fails, the account stays deleted and its name stays reserved;
some files may already have been removed. Correct the storage problem and retry
the purge to finish it. Files uploaded by the previous shared-password
implementation remain in the upload root; they are not automatically assigned
to an account.

# Shareable downloads

## Create and copy a link

Choose **Share** beside a file or folder, then **Create link** and **Copy**.
Anyone with that URL can download the item without an account. Files download
as attachments; folders download as ZIPs containing their current contents,
including files added after the link was created. Opening the Share dialog does
not enable sharing until you choose **Create link**.

## Replace or revoke a link

Each item has at most one link. Links survive restarts and do not expire
automatically. Only a SHA-256 hash of each link's token is stored, like session
tokens, so a leaked database or backup does not reveal working links. The URL
is therefore shown only once, right after **Create link**; copy it then.
Opening **Share** again shows that a link is active and offers **Replace link**,
which creates a new URL and disables the old one, or **Revoke link**. If
clipboard access is unavailable, the dialog selects the URL so you can copy it
manually.

## Changes that affect access

Renaming, moving, or deleting an item revokes links to that item and its
descendants, so recreating an old path cannot expose replacement files. Deleting
an account also revokes all its links while preserving its uploaded files.
Password resets leave sharing links active. Revocation blocks new downloads;
downloads already in progress can finish. Public tokens are omitted from the
application's request logs.

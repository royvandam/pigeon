# 0002. A JSON API for the browser interface and scripts

**Status:** Proposed · **Date:** 2026-10-25 · **Deciders:** maintainers

## Context

[0001](0001-frontend-framework.md) moves the interface to Preact islands, which
need data and actions as JSON. Today the HTTP API is a mix:

- **Forms and redirects:** file actions (`POST /files`), administration
  (`POST /admin`), sign-in, and sign-out post forms and answer with a redirect
  or a rendered page.
- **HTML fragments:** `GET /files` returns the file list as HTML for the
  explorer's script to insert.
- **JSON:** share links (`/shares`) and upload progress (`/upload-progress`,
  `/u/<token>/progress`).
- **Plain text:** uploads answer `ok` followed by `key=value` lines.
- **Errors:** plain text messages, mapped to statuses in several places
  (quality review Q5).

Scripts use the same routes with HTTP Basic authentication, so the API
reference documents all of these forms. Pigeon is not yet in production, so the
API may change without a compatibility layer.

## Decision

One JSON API under `/api`, used by the islands and by scripts alike. Routes
that serve browser navigation stay outside it.

### Conventions

- **Resources and methods:** resources are nouns; `GET` reads, `POST` creates or
  performs an action that is not an update, `PATCH` changes fields, `PUT`
  replaces, and `DELETE` removes. A file or folder is named by its path in the
  `path` query parameter, since paths contain slashes.
- **Bodies:** requests with a body send `Content-Type: application/json`, and
  other content types are refused with 415, except multipart uploads. Bodies
  are limited in size and decoded strictly: unknown fields are errors.
  Responses are `application/json` and `Cache-Control: no-store`.
- **Values:** sizes in bytes, times in RFC 3339 UTC, paths slash-separated and
  relative to the account's top folder. Formatting for people happens in the
  browser, in one place. Quotas are accepted as text such as `5 GB` and parsed
  by the server, also in one place.
- **Errors:** a status code and
  `{"error": {"code": "<code>", "message": "<sentence for people>"}}`. Codes
  are stable and documented; messages may change. One function maps the
  errors of Pigeon's packages to a status and code.
- **Authentication:** a session cookie from the sign-in page, or HTTP Basic
  authentication for scripts. Requests that change something with a session
  must send the session's CSRF token in `X-CSRF-Token` and pass the
  same-origin check; Basic requests need no token, as today.
- **Versioning:** none in the path. The interface and API ship in one binary,
  and breaking changes for scripts are listed in the release notes. A version
  prefix can be introduced if that ever stops being enough.

### Resources

| Method and route | Purpose |
| --- | --- |
| `GET /api/files?path=` | A folder's entries, breadcrumbs, and the account's usage |
| `POST /api/folders` | Create a folder: `{"parent": "...", "name": "..."}` |
| `PATCH /api/files?path=` | Rename, `{"name": "..."}`, or move, `{"folder": "..."}` |
| `DELETE /api/files?path=` | Delete a file or folder |
| `GET`, `PUT`, `DELETE /api/shares?path=&kind=` | A link's status, a new link with an optional lifetime, or revoking it |
| `POST /api/uploads?folder=` | A multipart upload, with an optional `X-Upload-ID` |
| `GET`, `DELETE /api/uploads/{id}` | An upload's progress, or canceling it |
| `GET`, `POST /api/users` | Administration: list accounts, create one |
| `PATCH`, `DELETE /api/users/{id}` | Change an account's quota or role, or delete it |
| `POST /api/users/{id}/password`, `/recycle`, `/purge` | Account actions that return a passphrase or remove files |
| `POST /u/{token}/uploads` | An upload through an upload link |
| `GET`, `DELETE /u/{token}/uploads/{id}` | Its progress, or canceling it |

Outside the API, unchanged: `/`, `/admin`, and `/u/{token}` serve page shells;
`/login` and `/logout` stay forms; `/download` and `/s/{token}` serve files;
`/assets/` serves the embedded assets.

## Consequences

- The forms, redirects, HTML fragments, and text responses above are removed as
  each island replaces them, together with the result-message maps in the
  handlers.
- One error mapping replaces the scattered ones, resolving quality review Q5.
  JSDoc types for the payloads give the browser code a checked description of
  the contract, which addresses Q8.
- The API reference is rewritten resource by resource as the routes change.
- Tests of handlers check JSON responses instead of rendered pages.

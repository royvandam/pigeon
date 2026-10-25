# Preact migration plan

**Status:** Not started · **Decisions:** [0001](../decisions/0001-frontend-framework.md),
[0002](../decisions/0002-json-api.md)

This plan moves Pigeon's browser interface to Preact islands and a JSON API, as
decided in 0001 and 0002. It is written so the work can stop and resume at any
point: each phase is one pull request, and [Progress](#progress) records where
things stand.

## Goal

When the plan is done:

- every interactive part of a page is a Preact island built from small,
  tested components and hooks;
- the islands and scripts use one JSON API with one error format;
- nothing of the old interface remains: no plain page scripts, HTML fragments,
  form-and-redirect handlers for actions, result-message maps, or tests of the
  old markup;
- the security policy, the single binary, and accessibility are at least as
  strong as today.

The migration keeps behavior as it is. New features, such as those in the
[functional review](../audit/2026-10-25-features.md), come after it.

## Rules for every phase

1. **One area per pull request.** A phase moves an area's API, interface, tests,
   and documentation together.
2. **Nothing twice.** The same pull request removes the code, templates, routes,
   tests, and CSS the area no longer uses. A temporary bridge between new and
   old code is allowed only when this plan names it and the phase that removes
   it.
3. **Every commit builds and passes.** CI, the vendored-file check, and the
   generated-CSS check pass on each commit.
4. **No unused code.** A module lands with its first user.
5. **Behavior is kept,** including focus handling, live announcements,
   keyboard use, and the existing messages, unless a phase says otherwise.
6. **Docs move with the code:** the API reference, architecture guide, and user
   guides change in the same pull request.

## Target architecture

### Pages and islands

```text
Go (pkg/server)                          Browser (web/static/app)
───────────────                          ────────────────────────
routes, sessions, CSRF, CSP              pages/explorer.js ─┐
page shells (Go templates)  ──HTML──▶    pages/admin.js     ├─ mount islands
  <div id="explorer" data-…>             pages/upload-link.js┘
/api/* JSON handlers  ◀──fetch/XHR──     features/* ─▶ ui/* ─▶ lib/*
/login, /logout forms                    vendor/preact.js, hooks.js, htm.js
/download, /s/{token}
```

- **Shells:** each page is a Go template with the layout, title, header, and an
  element per island. The element's `data-` attributes carry what the island
  needs to start, such as the CSRF token and the current folder. The island
  loads its data from the API, so loading and refreshing take the same path.
- **Server-rendered pages:** sign-in and the page for an upload link that no
  longer works need no island and stay plain templates.
- **Entry modules:** one module per page in `pages/` finds its elements, reads
  their attributes, and renders the islands. Each page loads its module with
  `<script type="module" src="…">`.

### Browser code

```text
web/static/
  vendor/        preact.js, hooks.js, htm.js, LICENSE files (generated, committed)
  app/
    lib/         html.js (htm bound to Preact), api.js, format.js, validation.js
    ui/          Dialog, ConfirmDialog, Field, Button, ProgressBar, Status
    features/
      files/     Explorer, FileList, Breadcrumbs, CreateFolder, file actions
      sharing/   ShareDialog, LinkSection
      uploads/   UploadPanel, DropZone, useUpload, readDropped
      admin/     Users, CreateUser, account actions, Passphrase
    pages/       explorer.js, admin.js, upload-link.js, login.js
```

- **Layers:** `pages` → `features` → `ui` → `lib` → `vendor`, and never the
  other way. A feature does not import another feature; a page composes them.
- **State:** component state and hooks, with `useReducer` where a feature has
  several related values. No global store and no extra state library.
- **API client:** `lib/api.js` is the only module that calls `fetch` or
  `XMLHttpRequest`. It adds the CSRF and `X-Requested-With` headers, decodes
  JSON, and turns error responses into an `ApiError` with the code and
  message. Features call typed functions such as `listFolder(path)`.
- **Types:** JSDoc types describe every API payload and module export, and
  `tsc --checkJs --noEmit` checks them in CI. No TypeScript is compiled or
  shipped.
- **Styling:** Tailwind classes in the components, as in the templates.
  Tailwind scans `web/static/app` recursively.
- **Naming:** files are kebab-case ES modules with named exports; components
  are PascalCase functions.

### Dependencies and vendoring

- `package.json` and `package-lock.json` pin `preact` and `htm` as runtime
  dependencies, and the development tools: `happy-dom` with
  `@happy-dom/global-registrator`, `@testing-library/preact`, and `typescript`.
- `npm ci --ignore-scripts` installs them; no package runs install scripts.
- `scripts/vendor-web.mjs` copies the published ES modules and licenses from
  `node_modules` into `web/static/vendor/`. It rewrites the hooks module's
  `from "preact"` to `from "./preact.js"` and fails unless exactly one import
  was rewritten.
- CI reinstalls, re-vendors, and fails when committed files differ. Dependabot
  watches npm weekly with a 7-day cooldown, like the other ecosystems.
- The runtime has no build step: what is vendored and written is what is
  served.

### Security invariants

- The Content Security Policy is unchanged: same-origin scripts and styles
  only, no inline code, no `eval`.
- Tests keep templates free of inline scripts, styles, and handlers. A new test
  rejects `eval`, `Function`, `innerHTML`, `outerHTML`, `insertAdjacentHTML`, and
  `dangerouslySetInnerHTML` in `web/static/app` and `web/static/vendor`.
- htm renders through Preact, which escapes text. Nothing inserts HTML strings.
- API requests that change something with a session need `X-CSRF-Token` and
  pass the same-origin check. JSON bodies need `Content-Type: application/json`,
  which plain cross-site forms cannot send.
- Share and upload-link tokens stay out of logs, as today.

### Testing

| Level | Tool | Covers |
| --- | --- | --- |
| Go handlers | `go test` | Every API route: success, each error code, CSRF, ownership |
| Browser units | `node --test` | `lib/` and hooks with fake `fetch` and `XMLHttpRequest` |
| Components | `node --test`, happy-dom, Testing Library | Rendering and behavior, found by role and label |
| Types | `tsc --checkJs` | API payloads and module contracts |
| End to end | Playwright with Chromium | Main journeys against the real binary (phase 6) |

Tests live in `test/web/` as `*.test.js` and import the same modules the
browser loads. The existing behavior tests in `test/browser/` are the
specification: each phase ports the tests for its area before removing them.

## Phases

### Phase 0: Decide and plan

Accept 0001, propose 0002, and add this plan. Documentation only.

**Done when** the pull request is merged and 0002 is reviewed.

### Phase 1: Foundation

Tooling only; no user-visible change and no application modules yet.

- `package.json`, lockfile, `.npmrc` with `ignore-scripts=true`, and
  `scripts/vendor-web.mjs` with `web/static/vendor/`.
- `web/embed.go` embeds the new directories; `web/tailwind.input.css` scans
  `web/static/app` recursively and ignores `vendor`.
- `tsconfig.json` for checking JavaScript, and the test setup that registers
  happy-dom.
- CI: install, re-vendor, and compare in the assets job; type check and run
  `test/web` beside `test/browser` in the browser job. Dependabot for npm.
- The security test for browser code, and one test that renders a component
  with the vendored Preact and htm, proving the pipeline end to end.
- Accept 0002. Update the contributing guide: setup, commands, and where
  things live.

**Done when** CI runs every new check and a vendored component renders in a
test.

### Phase 2a: JSON API foundation and the explorer

- **Go:** `pkg/server/api.go` with JSON response, error, and strict decoding
  helpers, the single error mapping, and API authentication: a session with
  `X-CSRF-Token` for changes, or Basic. Routes `GET /api/files`,
  `POST /api/folders`, `PATCH` and `DELETE /api/files`.
- **Browser:** `lib/html.js`, `lib/api.js`, `lib/format.js`; `ui/` Dialog,
  ConfirmDialog, Field, Button, Status; the Explorer island with usage,
  breadcrumbs, the file list, Create folder, and Rename, Move, and Delete.
- **Bridges:** each row's Share button keeps the `data-dialog-action="share"`
  attributes that `dialog.js` and `shares.js` handle, until 2b. After an
  upload, `upload.js` dispatches a `pigeon:files-changed` event that the
  Explorer reloads on, until phase 3. `upload.js` stops writing the storage
  summary, which the Explorer now renders from the API.
- **Removed:** `GET /files` fragments and `POST /files` actions, `files.html`,
  the file actions in `dialog.js`, the explorer's result messages, and their
  tests.

**Done when** the explorer works through the API only and its old tests are
ported and removed.

### Phase 2b: Sharing

- **Go:** `GET`, `PUT`, `DELETE /api/shares`.
- **Browser:** ShareDialog with a LinkSection per kind, opened from the
  Explorer.
- **Removed:** `/shares`, `shares.js`, `shares.html`, the share action in
  `dialog.js`, the Share bridge from 2a, and their tests.

### Phase 3: Uploads

- **Go:** `POST /api/uploads` with a JSON receipt, `GET` and
  `DELETE /api/uploads/{id}`, and the same under `/u/{token}/uploads`.
- **Browser:** `useUpload` holds selection, upload, progress, cancel, and its
  confirmation; `readDropped` reads dropped folders; UploadPanel and DropZone.
  The Explorer refreshes after an upload through a callback; the upload link
  page mounts UploadPanel alone.
- **Removed:** `/upload`, `/upload-progress`, `/u/{token}/progress`,
  `upload.js`, `uploads.html`, the `pigeon:files-changed` bridge, the text
  receipt, and their tests.

### Phase 4: Administration

- **Go:** `/api/users` routes, with passphrases only in the responses that
  create them.
- **Browser:** the Users island: the account table, Create account, and each
  account action with its confirmation, and the one-time passphrase display.
- **Removed:** `POST /admin` actions and their redirects and result messages,
  the form parts of `admin.html`, `dialog.js`, `dialog.html`, and their tests.

### Phase 5: Pages and leftovers

- Shared layout and header partials for the remaining templates, resolving
  quality review Q12.
- `validation.js` becomes `lib/validation.js`, used by `pages/login.js` and by
  Field, so form messages are defined once.
- Remove `test/browser/` and its harness once nothing uses them, and any CSS
  that no template or component uses.
- Move Font Awesome to the npm vendoring, so there is one way to vendor.

**Done when** `web/static` holds only `app/`, `vendor/`, generated CSS, and
images, and nothing references removed code.

### Phase 6: End-to-end tests

- Playwright with Chromium, in CI against the built binary with a temporary
  database and folder.
- Journeys: sign in, create a folder, upload and drop files and folders, rename,
  move, delete, share and download, upload through a link, cancel an upload,
  and manage an account.

**Done when** the journeys pass in CI, and break when their feature does.

### Phase 7: Close-out

- Check the architecture guide, API reference, user guides, and contributing
  guide against the code.
- Record resolutions in the quality review for Q5, Q8, Q12, and the parts of
  Q4 the API handlers address.
- Confirm 0001's consequences, set this plan to Done, and remove anything it
  marked as temporary.

## Definition of done

- [ ] No plain page scripts, and no use of `test/browser/`.
- [ ] No HTML fragments, form-and-redirect actions other than sign-in and
      sign-out, or result-message maps in handlers.
- [ ] One JSON error format and mapping, documented in the API reference.
- [ ] Each island has component tests, each API route has handler tests, and
      end-to-end journeys cover the main flows.
- [ ] Type checking, vendoring, generated CSS, and the security tests pass in
      CI.
- [ ] The Content Security Policy is unchanged, and no browser code inserts
      HTML strings.
- [ ] Keyboard use, focus handling, and announcements match or improve on
      today's.
- [ ] Documentation matches the code.

## Risks

| Risk | Mitigation |
| --- | --- |
| Preact 11 is new | Pin exact versions; behavior is covered by component and end-to-end tests; Preact 10 is a fallback with the same API |
| htm has had no release since 2022 | It is 1.2 KB with no dependencies; vendored and covered by tests, it can be kept as is |
| Rewriting the hooks import breaks on an upgrade | The vendoring script fails unless it rewrote exactly one import, and CI runs it |
| Behavior is lost in the move | Existing tests are ported before the old code is removed; end-to-end tests guard the journeys |
| npm adds supply-chain risk | Exact pins with a lockfile, no install scripts, Dependabot with a cooldown, and only the published modules are vendored |
| An interrupted session leaves work half done | One phase per pull request, commits that pass on their own, and the progress below |

## Effort

About three to four weeks of focused work: phase 1 one to two days; 2a three
to four; 2b two; 3 three to four; 4 three; 5 two; 6 two; 7 one.

## Progress

Update this section in each phase's pull request.

| Phase | Status | Pull request |
| --- | --- | --- |
| 0. Decide and plan | In review | — |
| 1. Foundation | Not started | — |
| 2a. JSON API and explorer | Not started | — |
| 2b. Sharing | Not started | — |
| 3. Uploads | Not started | — |
| 4. Administration | Not started | — |
| 5. Pages and leftovers | Not started | — |
| 6. End-to-end tests | Not started | — |
| 7. Close-out | Not started | — |

**To resume:** find the first phase that is not Done, check its branch and pull
request for what has landed, and continue from its scope and removals above.
Open temporary bridges: none yet.

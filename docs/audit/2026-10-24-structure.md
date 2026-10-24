# Package structure review

Reviewed commit `8e8779d` on 2026-10-24, after the
[code quality review](2026-10-20-quality.md)'s Q3 split the application into
packages under `pkg/`. The review looked for types and logic that sit in a
different package or layer than the code around them. It used the import graph
of every package and read where each package's types are built and used.
Behavior, performance, and security were out of scope.

## Summary

The split holds up. Each package has one clear responsibility, and dependencies
point from the HTTP layer down through the services and stores to the
database and storage. The configuration pattern, in which each part of
Pigeon defines the configuration type it builds from, is easy to follow.

What remains are places that do not follow those patterns yet:

1. **Configuration in the wrong place, or not built from.** The local storage
   provider's settings live in the package that chooses a provider, and the
   database takes a bare path instead of its configuration.
2. **Database details outside `pkg/database`.** A store returns
   `sql.ErrNoRows` to the HTTP layer, and another imports the SQLite driver.
3. **One handler doing service work.** The `/shares` handler validates paths,
   holds the account lock, and calls the share store itself.
4. **The account package reaching up** to the storage provider and usage cache
   to purge an account.

All but the last are small, and S1 to S5 were fixed together. S6 is left open:
the code works, is documented, and moving it is only worth doing alongside
other changes to account management.

## How to use this file

Work through open items one pull request at a time. When one is merged, set
its status to **Fixed** with the pull request number, in both the table and
the item, and add a **Resolution**. Use **Won't fix** with a short reason for
anything you decide to accept.

| ID | Impact | Area | Finding | Status |
| --- | --- | --- | --- | --- |
| [S1](#s1-local-storage-settings-live-outside-package-local) | Low | Configuration | Local storage settings live outside package `local` | Fixed in #23 |
| [S2](#s2-an-account-lookup-returns-sqlerrnorows-to-the-http-layer) | Low | Layering | An account lookup returns `sql.ErrNoRows` to the HTTP layer | Fixed in #23 |
| [S3](#s3-the-account-store-imports-the-sqlite-driver) | Low | Layering | The account store imports the SQLite driver | Fixed in #23 |
| [S4](#s4-the-database-does-not-follow-the-configuration-pattern) | Low | Configuration | The database does not follow the configuration pattern | Fixed in #23 |
| [S5](#s5-the-shares-handler-does-the-work-of-a-file-operation) | Medium | Separation of concerns | The `/shares` handler does the work of a file operation | Fixed in #23 |
| [S6](#s6-the-account-manager-depends-on-storage-and-the-usage-cache) | Low | Layering | The account manager depends on storage and the usage cache | Open |

---

## S1. Local storage settings live outside package `local`

**Impact:** Low · **Area:** Configuration · **Status:** Fixed in #23

**Location:** [pkg/storage/configuration/configuration.go:21](https://github.com/royvandam/pigeon/blob/8e8779d/pkg/storage/configuration/configuration.go#L21),
[pkg/storage/configuration/configuration.go:30](https://github.com/royvandam/pigeon/blob/8e8779d/pkg/storage/configuration/configuration.go#L30)

`LocalConfiguration`, and the check that its directory is set, are defined in
`pkg/storage/configuration`, the package that chooses between providers. Every
other part of Pigeon defines its configuration type in the package that builds
from it, such as `server.Configuration`. A second provider would add its
settings to the choosing package too, so that package would grow with every
provider's details.

`pkg/storage/configuration` itself must stay a separate package: package
`storage` cannot import the providers that implement its interfaces.

**Recommendation:** define `local.Configuration`, with `Validate` and
`NewProviderFromConfiguration`, in package `local`, and let the storage
configuration hold a `*local.Configuration` and delegate to it.

**Done when:** `pkg/storage/configuration` contains no provider's settings or
checks.

**Resolution:** `local.Configuration` holds the directory, and the storage
configuration only chooses a provider and delegates. The configuration file is
unchanged. The architecture guide explains why the choice is a package of its
own. A local test covers validation and construction.

---

## S2. An account lookup returns `sql.ErrNoRows` to the HTTP layer

**Impact:** Low · **Area:** Layering · **Status:** Fixed in #23

**Location:** [pkg/account/store.go:111](https://github.com/royvandam/pigeon/blob/8e8779d/pkg/account/store.go#L111),
[pkg/server/admin.go:115](https://github.com/royvandam/pigeon/blob/8e8779d/pkg/server/admin.go#L115)

`Store.DeletedIDForUsername` returns `sql.ErrNoRows` when no deleted account
reserves a username. The admin handler compares against it, so `pkg/server`
imports `database/sql` for that alone. Every other store method turns a
missing row into an account error, and `account.ErrDeletedUserNotFound`
already describes this case.

**Recommendation:** return `ErrDeletedUserNotFound` and check for it in the
handler.

**Done when:** `pkg/server` does not import `database/sql`.

**Resolution:** the lookup returns `ErrDeletedUserNotFound`, and an account
test covers an unreserved username.

---

## S3. The account store imports the SQLite driver

**Impact:** Low · **Area:** Layering · **Status:** Fixed in #23

**Location:** [pkg/account/store.go:9](https://github.com/royvandam/pigeon/blob/8e8779d/pkg/account/store.go#L9),
[pkg/account/store.go:58](https://github.com/royvandam/pigeon/blob/8e8779d/pkg/account/store.go#L58)

`Store.Create` recognizes a duplicate username by importing
`modernc.org/sqlite` and comparing the driver's error code. `pkg/database`
opens the database and should be the only package that knows which driver it
uses.

**Recommendation:** add a helper to `pkg/database` that reports a unique
constraint violation, and call it from the store.

**Done when:** only `pkg/database` imports `modernc.org/sqlite`.

**Resolution:** `database.IsUniqueViolation` does the check. A database test
checks that a duplicate row is a unique violation and that a CHECK failure is
not.

---

## S4. The database does not follow the configuration pattern

**Impact:** Low · **Area:** Configuration · **Status:** Fixed in #23

**Location:** [pkg/database/database.go:18](https://github.com/royvandam/pigeon/blob/8e8779d/pkg/database/database.go#L18),
[pkg/database/database.go:27](https://github.com/royvandam/pigeon/blob/8e8779d/pkg/database/database.go#L27),
[cmd/pigeon/main.go:95](https://github.com/royvandam/pigeon/blob/8e8779d/cmd/pigeon/main.go#L95)

`database.Configuration` has no `Validate`, and `database.Open` takes a bare
path rather than the configuration, unlike `server.New` and the storage
providers. `main` checks the HTTP and storage sections before it creates
anything but leaves the database path to `Open`. An empty path is still
rejected before anything is created, but only because `Open` happens to check
it first.

**Recommendation:** add `Configuration.Validate`, call it from `main` with
the other sections, and have `Open` take a `Configuration`.

**Done when:** `main` validates every section before opening the database, and
`Open` takes a `Configuration`.

**Resolution:** done as recommended; the tests open databases through a
`Configuration`.

---

## S5. The `/shares` handler does the work of a file operation

**Impact:** Medium · **Area:** Separation of concerns · **Status:** Fixed in #23

**Location:** [pkg/server/shares.go:15-77](https://github.com/royvandam/pigeon/blob/8e8779d/pkg/server/shares.go#L15-L77),
[pkg/server/server.go:62](https://github.com/royvandam/pigeon/blob/8e8779d/pkg/server/server.go#L62)

The handler validates the path, opens the account, checks that the item
exists while it holds the account lock, and chooses between the share store's
`Active`, `Create`, and `Revoke`. Every other file operation, including
opening a shared download, is a method in `pkg/files`, and their handlers only
parse requests and render responses. As a result:

- the rules for sharing (an existing item, never the top folder, removable
  after the item is gone) can only be tested through HTTP;
- the server takes the share store as a dependency next to the files service,
  so there are two ways to reach share links.

This is the kind of mixing that quality review finding
[Q4](2026-10-20-quality.md#q4-handlers-mix-http-orchestration-and-domain-rules)
describes for other handlers.

**Recommendation:** add share methods to `files.Access`, and have the handler
check the action, open the account, and call one of them.

**Done when:** the handler does not call the share store, and the server does
not depend on it.

**Resolution:** `files.Access` has `Shared`, `Share`, and `Unshare`, and
`server.Dependencies` no longer has `Shares`. Errors are answered like those
of other file operations. A files test covers the sharing rules.

---

## S6. The account manager depends on storage and the usage cache

**Impact:** Low · **Area:** Layering · **Status:** Open

**Location:** [pkg/account/manager.go:8](https://github.com/royvandam/pigeon/blob/8e8779d/pkg/account/manager.go#L8),
[pkg/account/manager.go:15](https://github.com/royvandam/pigeon/blob/8e8779d/pkg/account/manager.go#L15),
[pkg/account/manager.go:130](https://github.com/royvandam/pigeon/blob/8e8779d/pkg/account/manager.go#L130)

`account.Manager` coordinates account changes for administrators. To purge an
account it removes the account's files and forgets its cached usage, so it
holds a `storage.Provider` and a `*quota.Cache`, and `pkg/account` imports
`pkg/storage` and `pkg/quota` for them. `pkg/account` is otherwise the base
that `pkg/auth`, `pkg/share`, and `pkg/files` build on, so this is the one
place where it reaches up to storage. `pkg/files` already combines accounts,
storage, the usage cache, and the account locks.

It causes no problem today: the dependency is documented, there is no cycle,
and the manager is small. It would matter if account management grew more
cross-cutting work, or if storage ever needed to depend on accounts.

**Recommendation:** when account management next changes, move the manager,
or at least purging, to a package above both, such as `pkg/files` or a new
package for administration. `pkg/account` then keeps the account records,
locks, and passwords, and depends only on the database.

**Done when:** `pkg/account` does not import `pkg/storage`, and imports
`pkg/quota` only for quota values.

---

## What to keep

- **Configuration types in their packages,** with `Validate` that creates
  nothing and a constructor that takes the configuration.
- **A choice between implementations in a package of its own,** above the
  interface and every implementation, as `pkg/storage/configuration` is.
- **Stores that return domain errors,** so only `pkg/database` and the stores
  know about SQL.
- **Thin handlers** that parse a request, call one service method, map its
  error, and render.

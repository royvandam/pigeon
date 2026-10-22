# Development

## Environment

Enter `nix-shell`, or install the Go version declared in `go.mod` and Node.js
24 or newer. Use the repository root as the working directory for commands.
The [installation guide](../getting-started.md) covers running and building the
application, and the [architecture guide](architecture.md) maps its packages.
For remote editor setup, see [VS Code over SSH](editor.md).

## Checks and builds

Go tests run in parallel and hash passwords at bcrypt's minimum cost, so the
suite takes seconds; only a test that replaces the global log output stays
serial. Each package tests its own behavior; the HTTP tests in `pkg/server`
exercise them together through a test server whose fixture and shared helpers
live in `pkg/server/helpers_test.go`.

```sh
gofmt -l .
go mod tidy -diff
go test ./...
go test -race ./...
go vet ./...
node --test --test-isolation=none test/browser/*.test.cjs
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
go build -o bin/pigeon ./cmd/pigeon
```

GitHub Actions runs these checks for every pull request and push to `main`, and
runs `govulncheck` weekly so new advisories surface without code changes. CI
uses the Go version declared in `go.mod`. Dependabot proposes grouped weekly
updates for Go modules, documentation dependencies, and the pinned actions once
a release is a week old.
Dependabot does not update Go itself: when `govulncheck` reports a
standard-library vulnerability, raise the `go` line in `go.mod` to the patch
release that fixes it.

Run and build the command package (`./cmd/pigeon`) from the repository root.
Build output belongs in the ignored `bin/` directory.

Documentation changes also need a strict MkDocs build; see the
[documentation workflow](documentation.md). Before submitting a change, check
the diff for unrelated generated files or local data. Keep behavior changes,
generated assets, and their documentation together when they describe the same
change.

## Styles and icons

Pages load only assets embedded in the binary; `web/templates/assets.html`
links them and no page contacts a CDN. `web/static/tailwind.css` is generated
from the Tailwind classes in `web/templates/` and `web/static/*.js`. After
adding or changing classes, regenerate and commit it:

```sh
go generate ./web
```

This runs `scripts/tailwindcss.sh`, which downloads the pinned Tailwind CSS
standalone CLI on first use, verifies its SHA-256 checksum, and caches it under
`~/.cache/pigeon`. Font Awesome Free 7.1.0 is vendored in
`web/static/fontawesome/`: the core and solid styles, the solid font, and the
license. `scripts/vendor-fontawesome.sh` recreates these files from the
checksum-verified npm release. CI reruns both and fails if the committed files
differ. Dependabot does not track either version; to upgrade, change the version
and checksums in the script, rerun it, and commit the result.

`web/static/styles.css` sets fields, labels, buttons, and links to a consistent
14px font size and 20px line height, including file picker buttons.
The dialog and sharing templates supply markup; `web/static/dialog.js` and
`web/static/shares.js` handle interaction. `web/static/validation.js` replaces
native validation popups across all forms with shared, styled tooltips that do
not shift the layout, while keeping browser constraints and server-side checks.


## AI assistance

Pigeon is developed with the help of Claude Code, an AI coding assistant.

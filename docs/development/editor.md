# VS Code over SSH

Install the Go (`golang.go`) and
[Workspace Shared Settings](https://github.com/bohdan-shulha/vs-wss)
(`b-shulha.wss`) extensions on the remote SSH host. Open this project and run
**Workspace Shared Settings: Apply Workspace Settings** from the Command
Palette, then choose **Restart** when prompted, or run **Developer: Reload
Window**. The workspace must be trusted for Go to use its tool settings.

The extension expands `.vscode/settings.workspace.json` into the local,
Git-ignored `.vscode/settings.json`. The helper in `scripts/vscode-settings.sh`
enters this project's `shell.nix` on the remote host to discover the absolute
paths of `go`, `gopls`, and `goimports`. It also supplies the Nix shell's `PATH` through
`go.toolsEnvVars`, allowing `gopls` to find Go even when the SSH extension host
started outside `nix-shell`. Opening a Nix shell in the integrated terminal
alone does not update the running extension host's environment.

Go tools use `/tmp` for temporary files. Nix Env Selector can copy `TMPDIR`,
`TMP`, and `TEMP` from a short-lived Nix shell into the extension host, even
though that shell's temporary directory is removed when it exits. Overriding
these variables in `go.toolsEnvVars` prevents `go list` from failing with
`go: creating work dir: stat /tmp/nix-shell-…: no such file or directory` and
`gopls` from reporting **No packages found for open file**. After applying the
workspace settings, run **Go: Restart Language Server** to reload its environment.

Keep shared settings in `settings.workspace.json`; command substitutions are
expanded by Workspace Shared Settings rather than VS Code itself. Reapply the
workspace settings after changing `shell.nix` or updating your Nix packages.
Use **Go: Locate Configured Go Tools** to check the resolved paths.

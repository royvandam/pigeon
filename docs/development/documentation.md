# Documentation workflow

The project documentation lives in `docs/` and uses
[MkDocs](https://www.mkdocs.org/) with
[Material for MkDocs](https://squidfunk.github.io/mkdocs-material/).
`mkdocs.yml` defines navigation, branding, search, link validation, and the
GitHub Pages URL. The README stays focused on what Pigeon does and how to run
it; detailed instructions belong here.

## Preview locally

Use Python 3.12 or newer. Python is included in `nix-shell`; it is needed only
for documentation, not for building or running Pigeon. From the repository root:

```sh
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -r requirements-docs.txt
python -m mkdocs serve
```

Open <http://127.0.0.1:8000/pigeon/>. The `/pigeon/` prefix matches the
configured GitHub Pages project path. MkDocs rebuilds when documentation
changes. Stop it with Ctrl+C and leave the environment with `deactivate`.
There is no need to start the Go server to preview documentation.

## Validate a change

```sh
python -m mkdocs build --strict
```

The generated HTML is written to `site/`, which is ignored by Git. Strict mode
fails on missing navigation pages, broken internal links, and missing heading
anchors. It does not check whether external websites are available. Inspect
changed pages in the local preview as well, especially tables and code blocks.

## Write and organize pages

- Use a single `#` page title and descriptive `##` sections.
- Put task instructions in the user or administrator guides, endpoint details
  in the API reference, and implementation details under development.
- Link to documentation with relative `.md` paths. MkDocs converts these to
  site URLs, and the links also work when browsing the repository on GitHub.
- Link to application source with an absolute GitHub `blob/main/` URL, or a
  commit URL when describing historical behavior. Source files outside `docs/`
  are not part of the generated site.
- Add every new page to `nav` in `mkdocs.yml` and link it from a related guide.
- Use fenced code blocks with a language, and keep real credentials and
  instance-specific data out of examples.
- Keep the [audit archive](../audit/README.md) as historical records. Update
  resolutions without rewriting the findings.

The README uses `web/static/logo.webp`; the site has a copy at
`docs/assets/logo.webp` so local previews and deployments serve their own logo.
When changing the project logo, update both copies. The theme uses system
fonts, and its search assets are bundled with the site.

## Publish with GitHub Pages

The [Documentation workflow](https://github.com/royvandam/pigeon/blob/main/.github/workflows/docs.yml)
builds the site on pull requests and pushes to `main`. Pull requests validate
the build only. Successful builds on `main` upload the generated site and
deploy it through the `github-pages` environment; a manual workflow run on
`main` can redeploy it.

One-time repository setup:

1. Open **Settings → Pages** in the GitHub repository.
2. Under **Build and deployment → Source**, select **GitHub Actions**.
3. Merge the documentation configuration and workflow into `main`, or run
   **Actions → Documentation → Run workflow** on `main` if already merged.
4. Open the URL reported by the deployment job:
   <https://royvandam.github.io/pigeon/>.

The deployment job has `pages: write` and `id-token: write` permissions; build
jobs have only repository read access. The workflow publishes the `site/`
artifact, so a `gh-pages` branch and committed HTML are unnecessary. See
[GitHub's custom workflow guide](https://docs.github.com/en/pages/getting-started-with-github-pages/using-custom-workflows-with-github-pages)
for Pages settings and deployment permissions.

For a fork or a custom domain, update `site_url` in `mkdocs.yml` and the URL
references in these guides. If Pages reports a deployment error, confirm the
publishing source, Actions permissions, and `github-pages` environment rules
allow `main` to deploy.

## Update dependencies

Direct documentation dependencies are pinned in `requirements-docs.txt`.
Dependabot proposes grouped weekly pip updates alongside the existing Go and
Actions updates. After changing the pins, reinstall the requirements, run a
strict build, and inspect the local preview before committing.

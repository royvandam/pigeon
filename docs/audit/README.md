# Audits

Periodic reviews of Pigeon, kept so their findings can be worked through and
their outcomes traced later.

| Date | Kind | Commit | Findings | State |
| --- | --- | --- | --- | --- |
| 2026-10-17 | [Security](2026-10-17-security.md) | `636b71b` | 11 (1 high, 4 medium, 5 low, 1 informational) | All fixed |
| 2026-10-20 | [Code quality and design](2026-10-20-quality.md) | `845a0e1` | 12 (1 high, 5 medium, 6 low) | Open |
| 2026-10-24 | [Package structure](2026-10-24-structure.md) | `8e8779d` | 6 (1 medium, 5 low) | Open |

## Adding an audit

- Name the file `YYYY-MM-DD-<kind>.md`, using the date the review was done and
  a short kind such as `security` or `quality`. Add a row to the table above.
- State the commit reviewed and the scope, then summarize the overall picture.
- Give each finding an ID with the kind's letter and a number, such as A1 for
  security or Q1 for quality. Include an impact or severity, its location, the
  evidence or reproduction, a recommendation, and a "Done when" check.
- Keep a status table near the top that links to each finding.
- Link to source files with absolute GitHub URLs, so links work both on GitHub
  and on the documentation site. Pin each finding's location to the reviewed
  commit, such as `https://github.com/royvandam/pigeon/blob/845a0e1/internal/...`,
  so it keeps pointing at the reviewed code after files move or change. Link
  to current code, for example in a resolution, with a `blob/main/` URL.

## Working through findings

- Fix findings one pull request at a time, in the order the audit recommends.
- In the same pull request, set the finding's status to **Fixed in #N**, in both
  the table and the finding, and add a short **Resolution** describing what
  changed and how it was verified.
- Mark accepted risks **Won't fix** with the reason.
- When every finding is resolved, update the audit's state in the table above.
- Leave the findings themselves as they were written, so the reasoning stays
  traceable; record later decisions in the resolution instead.

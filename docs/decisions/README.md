# Decisions

Architecture decision records (ADRs) explain significant choices about how
Pigeon is built: the context, the options considered, what was decided, and
the consequences. They are kept so later changes can build on the reasoning
instead of rediscovering it.

| Number | Decision | Status |
| --- | --- | --- |
| [0001](0001-frontend-framework.md) | How to structure the browser interface as it grows | Proposed |

## Adding a decision

- Name the file `NNNN-short-title.md`, numbering from the last record, and add
  a row to the table above.
- Start with the status, the date, and who decided. A record is **Proposed**
  while it is open, then **Accepted** or **Rejected**. Mark a replaced decision
  **Superseded by NNNN** and link the record that replaces it.
- Describe the context and the forces at play, the options with their
  trade-offs, the decision, and its consequences.
- Once a record is accepted, leave it as it was decided. If the decision
  changes, write a new record that supersedes it.

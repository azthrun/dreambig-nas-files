# Moves stay inside one Mount

Rename and move are one atomic rename inside a single Mount. A move to another Mount is refused, and the folder picker never offers one. Each Mount is its own bind mount, so `rename(2)` across two fails with `EXDEV` even when they sit on the same NAS volume. Crossing would mean copy, verify, then Trash the source: a long job with progress, partial failure and a choice about what a half-finished move leaves behind. Today no such job exists, and every write in this service is either instant or an Upload.

A taken name is refused rather than settled with a Conflict choice, because no data is in flight that a refusal would waste: the User can rename and try again. A multi-item move checks every item first, so a clash changes nothing.

A symlink moves as the link. One with a relative target is refused, since it would point somewhere else or dangle, and a dangling link disappears from the listing. A relative link inside a moved folder is not checked. An Upload still running into a moved folder rebuilds its old Path when it completes, so nothing is lost but the file lands at the old location.

## Considered Options

- **Cross-Mount copy with a background job**: rejected for now; see above. Nothing here prevents adding it.
- **`Trash.Move` versus a new package**: the move lives in `internal/trash` because it needs that package's write lock and never-replace rule.

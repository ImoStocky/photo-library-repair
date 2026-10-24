# Chapter 18: Apply / amalgamate (S8)

**Read when:** changing materialization, `--move`, rename ordering, or
result reports.

**Names:** **amalgamate** is the product verb (plan → files in `--target`,
on demand). **apply** is the current CLI command and Go package for that
same step. Keep both names in docs; **do not rename the CLI while other
agents are working**. Sources and the SQLite library are not consumed.

**Status:** Partial — copy + `--move` + EXIF write; no rename-vacate /
`report.json`. Old-draft rename/commit actions are optional later.

## Responsibility

Materialize the plan: copy (or move) pixel files into the target
directory, write chosen metadata onto the copies, emit report. I/O.

## Spec summary

- Copy pixel source → `RESULT/stored_name`, then write capture/GPS/camera
  (and fill-in tags).
- `--move` relocates pixel source only.
- **new** refuses overwrite; **updated** overwrites; **renamed** moves
  existing library file (vacate / park-and-swap for cycles).
- Conflict never blocks materialize in the design (MVP skips conflicts
  unless `--force-conflicts`).

## Target package

`internal/apply` — `Apply(ctx, store, resultDir, move, dryRun)`,
`placeInOrder` for rename vacates.

## Current package

`internal/apply.Apply(store, Options)`:

- Options: target dir, dry-run, no-exif, force-conflicts, move, EXIF engine.
- Copies planned pixel images; unique-name helper on collision.
- Writes EXIF via the full-data engine (built-in writer as fallback).
- `Move` deletes the source file once its copy is fully in place. Duplicates
  inside a group and skipped conflict groups stay; a failed removal is
  reported (both files remain); a true dest == src is never followed by a
  deletion (same-file guard).
- Skips `StatusConflict` groups unless forced.
- No rename-vacate graph; no `report.json` (use `plr report`).

## Pseudocode (target)

```go
func Apply(ctx context.Context, s *Store, resultDir string, move, dryRun bool) error {
    plan := s.committedPlan()
    prefixAgainstExistingFiles(plan, resultDir)
    for _, p := range placeInOrder(plan, resultDir) {
        switch p.Action {
        case "untouched": continue
        case "new":       /* refuse if exists; transfer */
        case "renamed":   /* renameWithVacate */
        case "updated":   /* transfer if pixels changed */
        }
        writeChosenMeta(dest, p)
    }
    writeReport(resultDir, plan)
}
```

## CLI

```text
plr apply --target DIR [--dry-run] [--no-exif] [--move] [--force-conflicts] [--db PATH]
```

Documented alias name: **amalgamate** (same behavior; not wired as a
subcommand yet). `plr build` runs scan → group → plan → apply/amalgamate.

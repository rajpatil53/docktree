# Docktree Maintainer Notes

- The CLI enters through `cmd.Run(args, stdin, stdout, stderr)`. Keep command
  behavior testable through injected `commandDeps`.
- Shell, Docker, and Compose calls go through `internal/runner`; command tests use
  `fakeRunner` and should register every expected output command.
- Runtime state comes from Docker labels and Compose output. Do not add hidden
  per-worktree state files when `.docktree/.env.worktree`, generated projections, registry
  data, or Docker labels can carry the state.
- Compose projections are generated with `gopkg.in/yaml.v3` from the parsed
  `docker compose config --no-interpolate --no-normalize --format json` model.
- Port allocation is registry-backed and guarded by flock. `docktree up` also
  retries one worktree bind failure by bumping ports and rewriting artifacts.
- Stateful fork guards live in `internal/stateful`; Postgres SQL helpers live in
  `internal/dbmode` and validate rendered identifiers before shelling out.
- Standard verification:
  `go test -tags netgo ./...`,
  `go vet ./...`,
  `go test -tags netgo -coverprofile=/tmp/docktree.cover ./...`,
  `go tool cover -func=/tmp/docktree.cover`.

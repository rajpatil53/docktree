# Two-worktree smoke demo

This fixture is copied into a temporary Git repository by the smoke script. Do
not run Docktree directly in this directory: it sits inside the Docktree source
repository, so Git would resolve the source checkout as the application
worktree.

From the Docktree repository root, run the complete smoke test and remove its
resources afterward (Bash and curl are required in addition to Docktree's
normal development dependencies):

```bash
./scripts/smoke.sh
```

Keep both worktrees and their containers running for manual exploration:

```bash
./scripts/smoke.sh --keep
```

The command prints the temporary demo root, both worktree paths, their web
URLs, and the cleanup command. The retained root also contains:

- `docktree`: a wrapper around the freshly built binary. Always use this
  wrapper so the demo keeps its isolated Docktree registry and original Docker
  context.
- `docker`: a Docker wrapper targeting the same daemon as the setup run.
- `main/` and `feature/`: the primary and linked application worktrees.
- `destroy-demo`: a self-contained helper that stops both stacks, removes
  shared Redis and its volume, and removes the temporary demo root.
- `README.txt`: ready-to-copy commands for exploring the retained demo.

The smoke test verifies that the two worktrees receive different projects,
networks, and web ports; that the shared Redis container is not recreated; and
that a value written through the main worktree's uplink is readable through the
feature worktree's uplink.

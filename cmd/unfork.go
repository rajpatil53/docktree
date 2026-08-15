package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/rajpatil53/docktree/internal/dbmode"
	"github.com/rajpatil53/docktree/internal/stateful"
)

func Unfork(args []string) error {
	return runUnfork(context.Background(), args, defaultCommandDeps(os.Stdin, os.Stdout, os.Stderr))
}

func runUnfork(ctx context.Context, args []string, deps commandDeps) error {
	service, confirm, err := parseStatefulConfirmArgs("unfork", args)
	if err != nil {
		return err
	}
	wd, err := currentWorktreeRoot(deps)
	if err != nil {
		return err
	}
	return withAppCriticalSection(wd, deps, func() error {
		r, model, err := prepareStack(ctx, wd, deps)
		if err != nil {
			return err
		}
		st, err := renderedStatefulConfig(r, service)
		if err != nil {
			return err
		}
		if err := writeStackArtifacts(r, model, deps); err != nil {
			return err
		}
		if stateful.UsePostgresFastPath(st.Engine) {
			if _, err := applyPostgresLogicalForkModes(ctx, r, deps, true); err != nil {
				return err
			}
			if st.SourceDB == "" {
				return fmt.Errorf("stateful %s requires source_db for postgres forks", service)
			}
			db := stateful.TargetDB(st.SourceDB, r.slug)
			if confirm != db {
				return fmt.Errorf("docktree unfork %s requires --confirm %s", service, db)
			}
			if err := stateful.CheckDestroy(statefulDestroyRequest(r, service, stateful.ResourceLogicalDB, db)); err != nil {
				return err
			}
			if err := deps.runner.Run(ctx, worktreeComposeCommand(r, deps, "down")); err != nil {
				return err
			}
			dropSQL, err := dbmode.DropSQL(db)
			if err != nil {
				return err
			}
			if err := deps.runner.Run(ctx, postgresPsqlCommand(r, deps, service, dropSQL)); err != nil {
				return err
			}
		} else {
			volume := stateful.VolumeName(r.manifest.App, r.slug, service)
			if confirm != volume {
				return fmt.Errorf("docktree unfork %s requires --confirm %s", service, volume)
			}
			if err := stateful.CheckDestroy(statefulDestroyRequest(r, service, stateful.ResourceVolume, volume)); err != nil {
				return err
			}
			if err := deps.runner.Run(ctx, worktreeComposeCommand(r, deps, "down")); err != nil {
				return err
			}
			if err := deps.runner.Run(ctx, runnerCommand(r, deps, []string{"docker", "volume", "rm", volume})); err != nil {
				return err
			}
		}
		if err := writeStackArtifactsWithModes(r, model, deps, map[string]string{service: "shared"}); err != nil {
			return err
		}
		return recreateWorktreeStack(ctx, r, deps)
	})
}

func parseStatefulConfirmArgs(command string, args []string) (string, string, error) {
	if len(args) != 3 || args[1] != "--confirm" || args[0] == "" || args[2] == "" {
		if len(args) == 1 && args[0] != "" {
			return args[0], "", nil
		}
		return "", "", fmt.Errorf("usage: docktree %s <service> --confirm <resource>", command)
	}
	return args[0], args[2], nil
}

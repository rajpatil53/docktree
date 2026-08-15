package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/rajpatil53/docktree/internal/config"
	"github.com/rajpatil53/docktree/internal/dbmode"
	"github.com/rajpatil53/docktree/internal/paths"
	"github.com/rajpatil53/docktree/internal/runner"
	"github.com/rajpatil53/docktree/internal/stateful"
)

func Fork(args []string) error {
	return runFork(context.Background(), args, defaultCommandDeps(os.Stdin, os.Stdout, os.Stderr))
}

func runFork(ctx context.Context, args []string, deps commandDeps) error {
	service, err := parseStatefulServiceArgs("fork", args)
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
		if r.slug == r.mainSlug {
			return fmt.Errorf("refusing to fork main worktree %q", r.slug)
		}
		if err := writeStackArtifacts(r, model, deps); err != nil {
			return err
		}
		if stateful.UsePostgresFastPath(st.Engine) {
			if err := forkPostgres(ctx, r, service, st, deps); err != nil {
				return err
			}
		} else {
			if err := forkGenericVolume(ctx, r, service, st, deps); err != nil {
				return err
			}
		}
		if err := writeStackArtifactsWithModes(r, model, deps, map[string]string{service: "isolated"}); err != nil {
			return err
		}
		return recreateWorktreeStack(ctx, r, deps)
	})
}

func forkPostgres(ctx context.Context, r *resolved, service string, st config.Stateful, deps commandDeps) error {
	src := stateful.SourceDB(st.SourceDB)
	if src == "" {
		return fmt.Errorf("stateful %s requires source_db for postgres forks", service)
	}
	dst := stateful.TargetDB(src, r.slug)
	req := statefulDestroyRequest(r, service, stateful.ResourceLogicalDB, dst)
	req.Strategy = "isolated"
	if err := stateful.CheckDestroy(req); err != nil {
		return err
	}
	sourceExists, err := postgresDatabaseExists(ctx, r, deps, service, src)
	if err != nil {
		return err
	}
	if !sourceExists {
		return fmt.Errorf("source database %q does not exist; run docktree up from the main worktree first", src)
	}
	exists, err := postgresDatabaseExists(ctx, r, deps, service, dst)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("destination database %q already exists; refusing to overwrite it", dst)
	}
	fastSQL, err := dbmode.CloneFastSQL(src, dst)
	if err != nil {
		return err
	}
	if err := deps.runner.Run(ctx, postgresPsqlCommand(r, deps, service, fastSQL)); err == nil {
		return nil
	}
	dropSQL, err := dbmode.DropSQL(dst)
	if err != nil {
		return err
	}
	if err := deps.runner.Run(ctx, postgresPsqlCommand(r, deps, service, dropSQL)); err != nil {
		return err
	}
	fallback, err := dbmode.CloneFallbackCmd(src, dst, postgresSuperuser(r, service))
	if err != nil {
		return err
	}
	if err := deps.runner.Run(ctx, postgresShellCommand(r, deps, service, fallback)); err != nil {
		if cleanupErr := deps.runner.Run(ctx, postgresPsqlCommand(r, deps, service, dropSQL)); cleanupErr != nil {
			return fmt.Errorf("postgres fallback clone failed: %w; cleanup failed: %v", err, cleanupErr)
		}
		return err
	}
	return nil
}

func forkGenericVolume(ctx context.Context, r *resolved, service string, st config.Stateful, deps commandDeps) error {
	src := stateful.SnapshotSource(r.manifest.App, service, st.SnapshotSource)
	dst := stateful.VolumeName(r.manifest.App, r.slug, service)
	if err := stateful.ValidateVolumeName(src); err != nil {
		return err
	}
	if err := stateful.ValidateVolumeName(dst); err != nil {
		return err
	}
	req := statefulDestroyRequest(r, service, stateful.ResourceVolume, dst)
	req.Strategy = "isolated"
	if err := stateful.CheckDestroy(req); err != nil {
		return err
	}
	if err := inspectVolume(ctx, r, deps, src); err != nil {
		return fmt.Errorf("source volume %q is not available: %w", src, err)
	}
	if err := inspectVolume(ctx, r, deps, dst); err == nil {
		return fmt.Errorf("destination volume %q already exists; refusing to overwrite it", dst)
	}
	fmt.Fprintln(deps.stderr, stateful.QuiesceWarning(src))
	created := false
	if err := deps.runner.Run(ctx, runnerCommand(r, deps, stateful.VolumeCreateArgv(r.manifest.App, r.slug, r.names.Project, service))); err != nil {
		return err
	}
	created = true
	if err := deps.runner.Run(ctx, runnerCommand(r, deps, stateful.VolumeSnapshotArgv(src, dst))); err != nil {
		if created {
			if cleanupErr := deps.runner.Run(ctx, runnerCommand(r, deps, []string{"docker", "volume", "rm", dst})); cleanupErr != nil {
				return fmt.Errorf("snapshot %s to %s failed: %w; cleanup failed: %v", src, dst, err, cleanupErr)
			}
		}
		return err
	}
	return nil
}

func postgresDatabaseExists(ctx context.Context, r *resolved, deps commandDeps, service, db string) (bool, error) {
	sql, err := dbmode.DatabaseExistsSQL(db)
	if err != nil {
		return false, err
	}
	out, err := deps.runner.Output(ctx, postgresScalarCommand(r, deps, service, sql))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "1", nil
}

func inspectVolume(ctx context.Context, r *resolved, deps commandDeps, name string) error {
	command := runnerCommand(r, deps, []string{"docker", "volume", "inspect", name})
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return deps.runner.Run(ctx, command)
}

// postgresSuperuser resolves the Postgres role docktree connects as for a
// stateful service. It defaults to "postgres" (the official image's initdb
// superuser); apps whose POSTGRES_USER differs set [stateful.<service>].superuser.
func postgresSuperuser(r *resolved, service string) string {
	if st, ok := r.manifest.Stateful[service]; ok && st.Superuser != "" {
		return st.Superuser
	}
	return "postgres"
}

func postgresPsqlCommand(r *resolved, deps commandDeps, service, sql string) runner.Command {
	return composeCommand(r, deps, r.names.InfraProject, paths.InfraEnvFile(r.wd), paths.InfraCompose(r.wd), "exec", "-T", service, "psql", "-U", postgresSuperuser(r, service), "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-c", sql)
}

func postgresScalarCommand(r *resolved, deps commandDeps, service, sql string) runner.Command {
	return composeCommand(r, deps, r.names.InfraProject, paths.InfraEnvFile(r.wd), paths.InfraCompose(r.wd), "exec", "-T", service, "psql", "-U", postgresSuperuser(r, service), "-d", "postgres", "-tAc", sql)
}

func postgresShellCommand(r *resolved, deps commandDeps, service, script string) runner.Command {
	return composeCommand(r, deps, r.names.InfraProject, paths.InfraEnvFile(r.wd), paths.InfraCompose(r.wd), "exec", "-T", service, "sh", "-c", script)
}

func renderedStatefulConfig(r *resolved, service string) (config.Stateful, error) {
	st, ok := r.manifest.Stateful[service]
	if !ok {
		return config.Stateful{}, fmt.Errorf("stateful service %q is not configured", service)
	}
	ctx := config.TemplateContext{App: r.manifest.App, Slug: r.slug, MainSlug: r.mainSlug}
	st.DefaultStrategy = config.Expand(st.DefaultStrategy, ctx)
	st.Engine = config.Expand(st.Engine, ctx)
	st.SnapshotSource = config.Expand(st.SnapshotSource, ctx)
	st.SourceDB = config.Expand(st.SourceDB, ctx)
	return st, nil
}

func parseStatefulServiceArgs(command string, args []string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("usage: docktree %s <service>", command)
	}
	if args[0] == "" {
		return "", fmt.Errorf("usage: docktree %s <service>", command)
	}
	return args[0], nil
}

package run

import (
	gocontext "context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/vercel/turborepo/cli/internal/analytics"
	"github.com/vercel/turborepo/cli/internal/cache"
	"github.com/vercel/turborepo/cli/internal/config"
	"github.com/vercel/turborepo/cli/internal/context"
	"github.com/vercel/turborepo/cli/internal/core"
	"github.com/vercel/turborepo/cli/internal/fs"
	"github.com/vercel/turborepo/cli/internal/logstreamer"
	"github.com/vercel/turborepo/cli/internal/nodes"
	"github.com/vercel/turborepo/cli/internal/packagemanager"
	"github.com/vercel/turborepo/cli/internal/process"
	"github.com/vercel/turborepo/cli/internal/runcache"
	"github.com/vercel/turborepo/cli/internal/scm"
	"github.com/vercel/turborepo/cli/internal/scope"
	"github.com/vercel/turborepo/cli/internal/taskhash"
	"github.com/vercel/turborepo/cli/internal/ui"
	"github.com/vercel/turborepo/cli/internal/util"
	"github.com/vercel/turborepo/cli/internal/util/browser"

	"github.com/pyr-sh/dag"

	"github.com/fatih/color"
	"github.com/hashicorp/go-hclog"
	"github.com/mitchellh/cli"
	"github.com/pkg/errors"
)

// RunCommand is a Command implementation that tells Turbo to run a task
type RunCommand struct {
	Config    *config.Config
	Ui        *cli.ColoredUi
	Processes *process.Manager
}

// completeGraph represents the common state inferred from the filesystem and pipeline.
// It is not intended to include information specific to a particular run.
type completeGraph struct {
	TopologicalGraph dag.AcyclicGraph
	Pipeline         fs.Pipeline
	PackageInfos     map[interface{}]*fs.PackageJSON
	GlobalHash       string
	RootNode         string
}

// runSpec contains the run-specific configuration elements that come from a particular
// invocation of turbo.
type runSpec struct {
	Targets      []string
	FilteredPkgs util.Set
	Opts         *Opts
}

func (rs *runSpec) ArgsForTask(task string) []string {
	passThroughArgs := make([]string, 0, len(rs.Opts.runOpts.passThroughArgs))
	for _, target := range rs.Targets {
		if target == task {
			passThroughArgs = append(passThroughArgs, rs.Opts.runOpts.passThroughArgs...)
		}
	}
	return passThroughArgs
}

var _cmdLong = `
Run tasks across projects in your monorepo.

By default, turbo executes tasks in topological order (i.e.
dependencies first) and then caches the results. Re-running commands for
tasks already in the cache will skip re-execution and immediately move
artifacts from the cache into the correct output folders (as if the task
occurred again).

Arguments passed after '--' will be passed through to the named tasks.
`

func getCmd(config *config.Config, ui cli.Ui, processes *process.Manager) *cobra.Command {
	var opts *Opts
	cmd := &cobra.Command{
		Use:                   "turbo run <task> [...<task>] [<flags>] -- <args passed to tasks>",
		Short:                 "Run tasks across projects in your monorepo",
		Long:                  _cmdLong,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			tasks, passThroughArgs := parseTasksAndPassthroughArgs(args, cmd.Flags())
			if len(tasks) == 0 {
				return errors.New("at least one task must be specified")
			}
			opts.runOpts.passThroughArgs = passThroughArgs
			run := configureRun(config, ui, opts, processes)
			return run.run(tasks)
		},
	}
	opts = optsFromFlags(cmd.Flags(), config)
	return cmd
}

func parseTasksAndPassthroughArgs(remainingArgs []string, flags *pflag.FlagSet) ([]string, []string) {
	if argSplit := flags.ArgsLenAtDash(); argSplit != -1 {
		return remainingArgs[:argSplit], remainingArgs[argSplit:]
	}
	return remainingArgs, nil
}

func optsFromFlags(flags *pflag.FlagSet, config *config.Config) *Opts {
	opts := getDefaultOptions(config)
	aliases := make(map[string]string)
	scope.AddFlags(&opts.scopeOpts, flags)
	addRunOpts(&opts.runOpts, flags, aliases)
	noopPersistentOptsDuringMigration(flags)
	// TODO: this will probably have to change when we are all-cobra and might not
	// have Cwd yet.
	cache.AddFlags(&opts.cacheOpts, flags, config.Cwd)
	runcache.AddFlags(&opts.runcacheOpts, flags)
	flags.SetNormalizeFunc(func(f *pflag.FlagSet, name string) pflag.NormalizedName {
		if alias, ok := aliases[name]; ok {
			return pflag.NormalizedName(alias)
		}
		return pflag.NormalizedName(name)
	})
	return opts
}

func configureRun(config *config.Config, output cli.Ui, opts *Opts, processes *process.Manager) *run {
	if os.Getenv("TURBO_FORCE") == "true" {
		opts.runcacheOpts.SkipReads = true
	}

	if os.Getenv("TURBO_REMOTE_ONLY") == "true" {
		opts.cacheOpts.SkipFilesystem = true
	}

	if !config.IsLoggedIn() {
		opts.cacheOpts.SkipRemote = true
	}
	return &run{
		opts:      opts,
		config:    config,
		ui:        output,
		processes: processes,
	}
}

// Synopsis of run command
func (c *RunCommand) Synopsis() string {
	cmd := getCmd(c.Config, c.Ui, c.Processes)
	return cmd.Short
}

// Help returns information about the `run` command
func (c *RunCommand) Help() string {
	cmd := getCmd(c.Config, c.Ui, c.Processes)
	return util.HelpForCobraCmd(cmd)
}

// Run executes tasks in the monorepo
func (c *RunCommand) Run(args []string) int {
	cmd := getCmd(c.Config, c.Ui, c.Processes)
	cmd.SetArgs(args)
	err := cmd.Execute()
	if err != nil {
		exitErr := &process.ChildExit{}
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode
		}
		c.logError(c.Config.Logger, "", err)
		return 1
	}
	return 0
}

type run struct {
	opts      *Opts
	config    *config.Config
	ui        cli.Ui
	processes *process.Manager
}

func (r *run) run(targets []string) error {
	startAt := time.Now()
	ctx, err := context.New(context.WithGraph(r.config, r.opts.cacheOpts.Dir))
	if err != nil {
		return err
	}

	if err := util.ValidateGraph(&ctx.TopologicalGraph); err != nil {
		return errors.Wrap(err, "Invalid package dependency graph")
	}

	pipeline := r.config.TurboJSON.Pipeline
	if err := validateTasks(pipeline, targets); err != nil {
		return err
	}

	scmInstance, err := scm.FromInRepo(r.config.Cwd.ToStringDuringMigration())
	if err != nil {
		if errors.Is(err, scm.ErrFallback) {
			r.logWarning("", err)
		} else {
			return errors.Wrap(err, "failed to create SCM")
		}
	}
	filteredPkgs, isAllPackages, err := scope.ResolvePackages(&r.opts.scopeOpts, r.config.Cwd.ToStringDuringMigration(), scmInstance, ctx, r.ui, r.config.Logger)
	if err != nil {
		return errors.Wrap(err, "failed to resolve packages to run")
	}
	if isAllPackages {
		// if there is a root task for any of our targets, we need to add it
		for _, target := range targets {
			key := util.RootTaskID(target)
			if _, ok := pipeline[key]; ok {
				filteredPkgs.Add(util.RootPkgName)
				// we only need to know we're running a root task once to add it for consideration
				break
			}
		}
	}
	r.config.Logger.Debug("global hash", "value", ctx.GlobalHash)
	r.config.Logger.Debug("local cache folder", "path", r.opts.cacheOpts.Dir)

	// TODO: consolidate some of these arguments
	g := &completeGraph{
		TopologicalGraph: ctx.TopologicalGraph,
		Pipeline:         pipeline,
		PackageInfos:     ctx.PackageInfos,
		GlobalHash:       ctx.GlobalHash,
		RootNode:         ctx.RootNode,
	}
	rs := &runSpec{
		Targets:      targets,
		FilteredPkgs: filteredPkgs,
		Opts:         r.opts,
	}
	packageManager := ctx.PackageManager
	return r.runOperation(g, rs, packageManager, startAt)
}

func (r *run) runOperation(g *completeGraph, rs *runSpec, packageManager *packagemanager.PackageManager, startAt time.Time) error {
	vertexSet := make(util.Set)
	for _, v := range g.TopologicalGraph.Vertices() {
		vertexSet.Add(v)
	}

	engine, err := buildTaskGraph(&g.TopologicalGraph, g.Pipeline, rs)
	if err != nil {
		return errors.Wrap(err, "error preparing engine")
	}
	hashTracker := taskhash.NewTracker(g.RootNode, g.GlobalHash, g.Pipeline, g.PackageInfos)
	err = hashTracker.CalculateFileHashes(engine.TaskGraph.Vertices(), rs.Opts.runOpts.concurrency, r.config.Cwd)
	if err != nil {
		return errors.Wrap(err, "error hashing package files")
	}

	// If we are running in parallel, then we remove all the edges in the graph
	// except for the root. Rebuild the task graph for backwards compatibility.
	// We still use dependencies specified by the pipeline configuration.
	if rs.Opts.runOpts.parallel {
		for _, edge := range g.TopologicalGraph.Edges() {
			if edge.Target() != g.RootNode {
				g.TopologicalGraph.RemoveEdge(edge)
			}
		}
		engine, err = buildTaskGraph(&g.TopologicalGraph, g.Pipeline, rs)
		if err != nil {
			return errors.Wrap(err, "error preparing engine")
		}
	}

	if rs.Opts.runOpts.dotGraph != "" {
		if err := r.generateDotGraph(engine.TaskGraph, r.config.Cwd.Join(rs.Opts.runOpts.dotGraph)); err != nil {
			return err
		}
	} else if rs.Opts.runOpts.dryRun {
		tasksRun, err := r.executeDryRun(engine, g, hashTracker, rs)
		if err != nil {
			return err
		}
		packagesInScope := rs.FilteredPkgs.UnsafeListOfStrings()
		sort.Strings(packagesInScope)
		if rs.Opts.runOpts.dryRunJSON {
			dryRun := &struct {
				Packages []string     `json:"packages"`
				Tasks    []hashedTask `json:"tasks"`
			}{
				Packages: packagesInScope,
				Tasks:    tasksRun,
			}
			bytes, err := json.MarshalIndent(dryRun, "", "  ")
			if err != nil {
				return errors.Wrap(err, "failed to render JSON")
			}
			r.ui.Output(string(bytes))
		} else {
			r.ui.Output("")
			r.ui.Info(util.Sprintf("${CYAN}${BOLD}Packages in Scope${RESET}"))
			p := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
			fmt.Fprintln(p, "Name\tPath\t")
			for _, pkg := range packagesInScope {
				fmt.Fprintf(p, "%s\t%s\t\n", pkg, g.PackageInfos[pkg].Dir)
			}
			p.Flush()

			r.ui.Output("")
			r.ui.Info(util.Sprintf("${CYAN}${BOLD}Tasks to Run${RESET}"))

			for _, task := range tasksRun {
				r.ui.Info(util.Sprintf("${BOLD}%s${RESET}", task.TaskID))
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
				fmt.Fprintln(w, util.Sprintf("  ${GREY}Task\t=\t%s\t${RESET}", task.Task))
				fmt.Fprintln(w, util.Sprintf("  ${GREY}Package\t=\t%s\t${RESET}", task.Package))
				fmt.Fprintln(w, util.Sprintf("  ${GREY}Hash\t=\t%s\t${RESET}", task.Hash))
				fmt.Fprintln(w, util.Sprintf("  ${GREY}Directory\t=\t%s\t${RESET}", task.Dir))
				fmt.Fprintln(w, util.Sprintf("  ${GREY}Command\t=\t%s\t${RESET}", task.Command))
				fmt.Fprintln(w, util.Sprintf("  ${GREY}Outputs\t=\t%s\t${RESET}", strings.Join(task.Outputs, ", ")))
				fmt.Fprintln(w, util.Sprintf("  ${GREY}Log File\t=\t%s\t${RESET}", task.LogFile))
				fmt.Fprintln(w, util.Sprintf("  ${GREY}Dependencies\t=\t%s\t${RESET}", strings.Join(task.Dependencies, ", ")))
				fmt.Fprintln(w, util.Sprintf("  ${GREY}Dependendents\t=\t%s\t${RESET}", strings.Join(task.Dependents, ", ")))
				w.Flush()
			}
		}
	} else {
		packagesInScope := rs.FilteredPkgs.UnsafeListOfStrings()
		sort.Strings(packagesInScope)
		r.ui.Output(fmt.Sprintf(ui.Dim("• Packages in scope: %v"), strings.Join(packagesInScope, ", ")))
		r.ui.Output(fmt.Sprintf("%s %s %s", ui.Dim("• Running"), ui.Dim(ui.Bold(strings.Join(rs.Targets, ", "))), ui.Dim(fmt.Sprintf("in %v packages", rs.FilteredPkgs.Len()))))
		return r.executeTasks(g, rs, engine, packageManager, hashTracker, startAt)
	}
	return nil
}

func buildTaskGraph(topoGraph *dag.AcyclicGraph, pipeline fs.Pipeline, rs *runSpec) (*core.Scheduler, error) {
	engine := core.NewScheduler(topoGraph)
	for taskName, taskDefinition := range pipeline {
		topoDeps := make(util.Set)
		deps := make(util.Set)
		isPackageTask := util.IsPackageTask(taskName)
		for _, dependency := range taskDefinition.TaskDependencies {
			if isPackageTask && util.IsPackageTask(dependency) {
				err := engine.AddDep(dependency, taskName)
				if err != nil {
					return nil, err
				}
			} else {
				deps.Add(dependency)
			}
		}
		for _, dependency := range taskDefinition.TopologicalDependencies {
			topoDeps.Add(dependency)
		}
		engine.AddTask(&core.Task{
			Name:     taskName,
			TopoDeps: topoDeps,
			Deps:     deps,
		})
	}

	if err := engine.Prepare(&core.SchedulerExecutionOptions{
		Packages:  rs.FilteredPkgs.UnsafeListOfStrings(),
		TaskNames: rs.Targets,
		TasksOnly: rs.Opts.runOpts.only,
	}); err != nil {
		return nil, err
	}

	if err := util.ValidateGraph(engine.TaskGraph); err != nil {
		return nil, fmt.Errorf("Invalid task dependency graph:\n%v", err)
	}

	return engine, nil
}

// Opts holds the current run operations configuration
type Opts struct {
	runOpts      runOpts
	cacheOpts    cache.Opts
	runcacheOpts runcache.Opts
	scopeOpts    scope.Opts
}

// runOpts holds the options that control the execution of a turbo run
type runOpts struct {
	// Show a dot graph
	dotGraph string
	// Force execution to be serially one-at-a-time
	concurrency int
	// Whether to execute in parallel (defaults to false)
	parallel bool
	// Whether to emit a perf profile
	profile string
	// If true, continue task executions even if a task fails.
	continueOnError bool
	passThroughArgs []string
	// Restrict execution to only the listed task names. Default false
	only       bool
	dryRun     bool
	dryRunJSON bool
}

var (
	_profileHelp = `File to write turbo's performance profile output into.
You can load the file up in chrome://tracing to see
which parts of your build were slow.`
	_continueHelp = `Continue execution even if a task exits with an error
or non-zero exit code. The default behavior is to bail`
	_dryRunHelp = `List the packages in scope and the tasks that would be run,
but don't actually run them. Passing --dry=json or
--dry-run=json will render the output in JSON format.`
)

func addRunOpts(opts *runOpts, flags *pflag.FlagSet, aliases map[string]string) {
	flags.StringVar(&opts.dotGraph, "graph", "", "Generate a Dot graph of the task execution.")
	flags.IntVar(&opts.concurrency, "concurrency", 10, "Limit the concurrency of task execution. Use 1 for serial (i.e. one-at-a-time) execution.")
	flags.BoolVar(&opts.parallel, "parallel", false, "Execute all tasks in parallel.")
	flags.StringVar(&opts.profile, "profile", "", _profileHelp)
	flags.BoolVar(&opts.continueOnError, "continue", false, _continueHelp)
	flags.BoolVar(&opts.only, "only", false, "Run only the specified tasks, not their dependencies")
	if err := flags.MarkHidden("only"); err != nil {
		// fail fast if we've messed up our flag configuration
		panic(err)
	}
	aliases["dry"] = "dry-run"
	flags.AddFlag(&pflag.Flag{
		Name:        "dry-run",
		Usage:       _dryRunHelp,
		DefValue:    "",
		NoOptDefVal: "text|json",
		Value:       &dryRunValue{opts: opts},
	})
}

var _persistentFlags = []string{
	"team",
	"token",
	"preflight",
	"api",
	"url",
	"trace",
	"cpuprofile",
	"heap",
	"no-gc",
	"cwd",
}

func noopPersistentOptsDuringMigration(flags *pflag.FlagSet) {
	for _, flag := range _persistentFlags {
		_ = flags.String(flag, "", "")
		if err := flags.MarkHidden(flag); err != nil {
			// fail fast if we've misconfigured our flags
			panic(err)
		}
	}
}

type dryRunValue struct {
	opts *runOpts
}

var _ pflag.Value = &dryRunValue{}

func (d *dryRunValue) String() string {
	if d.opts.dryRunJSON {
		return "json"
	} else if d.opts.dryRun {
		return "dry run"
	}
	return ""
}

func (d *dryRunValue) Set(value string) error {
	if value == "json" {
		d.opts.dryRun = true
		d.opts.dryRunJSON = true
	} else if value == "text|json" {
		d.opts.dryRun = true
	} else {
		return fmt.Errorf("invalid dry-run mode: %v", value)
	}
	return nil
}

func (d *dryRunValue) Type() string {
	return "/ dry "
}

func getDefaultOptions(config *config.Config) *Opts {
	return &Opts{
		runOpts: runOpts{
			concurrency: 10,
		},
		cacheOpts: cache.Opts{
			Dir:     cache.DefaultLocation(config.Cwd),
			Workers: config.Cache.Workers,
		},
		runcacheOpts: runcache.Opts{
			TaskOutputMode: config.TurboJSON.OutputLogs,
		},
		scopeOpts: scope.Opts{},
	}
}

// logError logs an error and outputs it to the UI.
func (c *RunCommand) logError(log hclog.Logger, prefix string, err error) {
	log.Error(prefix, "error", err)

	if prefix != "" {
		prefix += ": "
	}

	c.Ui.Error(fmt.Sprintf("%s%s%s", ui.ERROR_PREFIX, prefix, color.RedString(" %v", err)))
}

// logError logs an error and outputs it to the UI.
func (r *run) logWarning(prefix string, err error) {
	r.config.Logger.Warn(prefix, "warning", err)

	if prefix != "" {
		prefix = " " + prefix + ": "
	}

	r.ui.Error(fmt.Sprintf("%s%s%s", ui.WARNING_PREFIX, prefix, color.YellowString(" %v", err)))
}

func hasGraphViz() bool {
	err := exec.Command("dot", "-v").Run()
	return err == nil
}

func (r *run) executeTasks(g *completeGraph, rs *runSpec, engine *core.Scheduler, packageManager *packagemanager.PackageManager, hashes *taskhash.Tracker, startAt time.Time) error {
	goctx := gocontext.Background()
	var analyticsSink analytics.Sink
	if r.config.IsLoggedIn() {
		analyticsSink = r.config.ApiClient
	} else {
		analyticsSink = analytics.NullSink
	}
	analyticsClient := analytics.NewClient(goctx, analyticsSink, r.config.Logger.Named("analytics"))
	defer analyticsClient.CloseWithTimeout(50 * time.Millisecond)
	// Theoretically this is overkill, but bias towards not spamming the console
	once := &sync.Once{}
	turboCache, err := cache.New(rs.Opts.cacheOpts, r.config, analyticsClient, func(_cache cache.Cache, err error) {
		// Currently the HTTP Cache is the only one that can be disabled.
		// With a cache system refactor, we might consider giving names to the caches so
		// we can accurately report them here.
		once.Do(func() {
			r.logWarning("Remote Caching is unavailable", err)
		})
	})
	if err != nil {
		if errors.Is(err, cache.ErrNoCachesEnabled) {
			r.logWarning("No caches are enabled. You can try \"turbo login\", \"turbo link\", or ensuring you are not passing --remote-only to enable caching", nil)
		} else {
			return errors.Wrap(err, "failed to set up caching")
		}
	}
	defer turboCache.Shutdown()
	runState := NewRunState(startAt, rs.Opts.runOpts.profile)
	runCache := runcache.New(turboCache, r.config.Cwd, rs.Opts.runcacheOpts)
	ec := &execContext{
		colorCache:     NewColorCache(),
		runState:       runState,
		rs:             rs,
		ui:             &cli.ConcurrentUi{Ui: r.ui},
		turboCache:     turboCache,
		runCache:       runCache,
		logger:         r.config.Logger,
		packageManager: packageManager,
		processes:      r.processes,
		taskHashes:     hashes,
	}

	// run the thing
	errs := engine.Execute(g.getPackageTaskVisitor(func(pt *nodes.PackageTask) error {
		deps := engine.TaskGraph.DownEdges(pt.TaskID)
		return ec.exec(pt, deps)
	}), core.ExecOpts{
		Parallel:    rs.Opts.runOpts.parallel,
		Concurrency: rs.Opts.runOpts.concurrency,
	})

	// Track if we saw any child with a non-zero exit code
	exitCode := 0
	exitCodeErr := &process.ChildExit{}
	for _, err := range errs {
		if errors.As(err, &exitCodeErr) {
			if exitCodeErr.ExitCode > exitCode {
				exitCode = exitCodeErr.ExitCode
			}
		} else if exitCode == 0 {
			// We hit some error, it shouldn't be exit code 0
			exitCode = 1
		}
		r.ui.Error(err.Error())
	}

	if err := runState.Close(r.ui, rs.Opts.runOpts.profile); err != nil {
		return errors.Wrap(err, "error with profiler")
	}
	if exitCode != 0 {
		return &process.ChildExit{
			ExitCode: exitCode,
		}
	}
	return nil
}

type hashedTask struct {
	TaskID       string   `json:"taskId"`
	Task         string   `json:"task"`
	Package      string   `json:"package"`
	Hash         string   `json:"hash"`
	Command      string   `json:"command"`
	Outputs      []string `json:"outputs"`
	LogFile      string   `json:"logFile"`
	Dir          string   `json:"directory"`
	Dependencies []string `json:"dependencies"`
	Dependents   []string `json:"dependents"`
}

func (r *run) executeDryRun(engine *core.Scheduler, g *completeGraph, taskHashes *taskhash.Tracker, rs *runSpec) ([]hashedTask, error) {
	taskIDs := []hashedTask{}
	errs := engine.Execute(g.getPackageTaskVisitor(func(pt *nodes.PackageTask) error {
		passThroughArgs := rs.ArgsForTask(pt.Task)
		deps := engine.TaskGraph.DownEdges(pt.TaskID)
		hash, err := taskHashes.CalculateTaskHash(pt, deps, passThroughArgs)
		if err != nil {
			return err
		}
		command, ok := pt.Command()
		if !ok {
			command = "<NONEXISTENT>"
		}
		isRootTask := pt.PackageName == util.RootPkgName
		if isRootTask && commandLooksLikeTurbo(command) {
			return fmt.Errorf("root task %v (%v) looks like it invokes turbo and might cause a loop", pt.Task, command)
		}
		ancestors, err := engine.TaskGraph.Ancestors(pt.TaskID)
		if err != nil {
			return err
		}
		stringAncestors := []string{}
		for _, dep := range ancestors {
			// Don't leak out internal ROOT_NODE_NAME nodes, which are just placeholders
			if !strings.Contains(dep.(string), core.ROOT_NODE_NAME) {
				stringAncestors = append(stringAncestors, dep.(string))
			}
		}
		descendents, err := engine.TaskGraph.Descendents(pt.TaskID)
		if err != nil {
			return err
		}
		stringDescendents := []string{}
		for _, dep := range descendents {
			// Don't leak out internal ROOT_NODE_NAME nodes, which are just placeholders
			if !strings.Contains(dep.(string), core.ROOT_NODE_NAME) {
				stringDescendents = append(stringDescendents, dep.(string))
			}
		}
		sort.Strings(stringDescendents)

		taskIDs = append(taskIDs, hashedTask{
			TaskID:       pt.TaskID,
			Task:         pt.Task,
			Package:      pt.PackageName,
			Hash:         hash,
			Command:      command,
			Dir:          pt.Pkg.Dir,
			Outputs:      pt.TaskDefinition.Outputs,
			LogFile:      pt.RepoRelativeLogFile(),
			Dependencies: stringAncestors,
			Dependents:   stringDescendents,
		})
		return nil
	}), core.ExecOpts{
		Concurrency: 1,
		Parallel:    false,
	})
	if len(errs) > 0 {
		for _, err := range errs {
			r.ui.Error(err.Error())
		}
		return nil, errors.New("errors occurred during dry-run graph traversal")
	}
	return taskIDs, nil
}

var _isTurbo = regexp.MustCompile(fmt.Sprintf("(?:^|%v|\\s)turbo(?:$|\\s)", regexp.QuoteMeta(string(filepath.Separator))))

func commandLooksLikeTurbo(command string) bool {
	return _isTurbo.MatchString(command)
}

func validateTasks(pipeline fs.Pipeline, tasks []string) error {
	for _, task := range tasks {
		if !pipeline.HasTask(task) {
			return fmt.Errorf("task `%v` not found in turbo `pipeline` in \"turbo.json\". Are you sure you added it?", task)
		}
	}
	return nil
}

type execContext struct {
	colorCache     *ColorCache
	runState       *RunState
	rs             *runSpec
	ui             cli.Ui
	runCache       *runcache.RunCache
	turboCache     cache.Cache
	logger         hclog.Logger
	packageManager *packagemanager.PackageManager
	processes      *process.Manager
	taskHashes     *taskhash.Tracker
}

func (e *execContext) logError(log hclog.Logger, prefix string, err error) {
	e.logger.Error(prefix, "error", err)

	if prefix != "" {
		prefix += ": "
	}

	e.ui.Error(fmt.Sprintf("%s%s%s", ui.ERROR_PREFIX, prefix, color.RedString(" %v", err)))
}

func (e *execContext) exec(pt *nodes.PackageTask, deps dag.Set) error {
	cmdTime := time.Now()

	targetLogger := e.logger.Named(pt.OutputPrefix())
	targetLogger.Debug("start")

	// Setup tracer
	tracer := e.runState.Run(pt.TaskID)

	// Create a logger
	pref := e.colorCache.PrefixColor(pt.PackageName)
	actualPrefix := pref("%s: ", pt.OutputPrefix())
	targetUi := &cli.PrefixedUi{
		Ui:           e.ui,
		OutputPrefix: actualPrefix,
		InfoPrefix:   actualPrefix,
		ErrorPrefix:  actualPrefix,
		WarnPrefix:   actualPrefix,
	}

	passThroughArgs := e.rs.ArgsForTask(pt.Task)
	hash, err := e.taskHashes.CalculateTaskHash(pt, deps, passThroughArgs)
	e.logger.Debug("task hash", "value", hash)
	if err != nil {
		e.ui.Error(fmt.Sprintf("Hashing error: %v", err))
		// @TODO probably should abort fatally???
	}
	// TODO(gsoltis): if/when we fix https://github.com/vercel/turborepo/issues/937
	// the following block should never get hit. In the meantime, keep it after hashing
	// so that downstream tasks can count on the hash existing
	//
	// bail if the script doesn't exist
	if _, ok := pt.Command(); !ok {
		targetLogger.Debug("no task in package, skipping")
		targetLogger.Debug("done", "status", "skipped", "duration", time.Since(cmdTime))
		return nil
	}
	// Cache ---------------------------------------------
	taskCache := e.runCache.TaskCache(pt, hash)
	hit, err := taskCache.RestoreOutputs(targetUi, targetLogger)
	if err != nil {
		targetUi.Error(fmt.Sprintf("error fetching from cache: %s", err))
	} else if hit {
		tracer(TargetCached, nil)
		return nil
	}
	// Setup command execution
	argsactual := append([]string{"run"}, pt.Task)
	argsactual = append(argsactual, passThroughArgs...)

	cmd := exec.Command(e.packageManager.Command, argsactual...)
	cmd.Dir = pt.Pkg.Dir
	envs := fmt.Sprintf("TURBO_HASH=%v", hash)
	cmd.Env = append(os.Environ(), envs)

	// Setup stdout/stderr
	// If we are not caching anything, then we don't need to write logs to disk
	// be careful about this conditional given the default of cache = true
	writer, err := taskCache.OutputWriter()
	if err != nil {
		tracer(TargetBuildFailed, err)
		e.logError(targetLogger, actualPrefix, err)
		if !e.rs.Opts.runOpts.continueOnError {
			os.Exit(1)
		}
	}
	logger := log.New(writer, "", 0)
	// Setup a streamer that we'll pipe cmd.Stdout to
	logStreamerOut := logstreamer.NewLogstreamer(logger, actualPrefix, false)
	// Setup a streamer that we'll pipe cmd.Stderr to.
	logStreamerErr := logstreamer.NewLogstreamer(logger, actualPrefix, false)
	cmd.Stderr = logStreamerErr
	cmd.Stdout = logStreamerOut
	// Flush/Reset any error we recorded
	logStreamerErr.FlushRecord()
	logStreamerOut.FlushRecord()
	closeOutputs := func() error {
		var closeErrors []error
		if err := logStreamerOut.Close(); err != nil {
			closeErrors = append(closeErrors, errors.Wrap(err, "log stdout"))
		}
		if err := logStreamerErr.Close(); err != nil {
			closeErrors = append(closeErrors, errors.Wrap(err, "log stderr"))
		}
		if err := writer.Close(); err != nil {
			closeErrors = append(closeErrors, errors.Wrap(err, "log file"))
		}
		if len(closeErrors) > 0 {
			msgs := make([]string, len(closeErrors))
			for i, err := range closeErrors {
				msgs[i] = err.Error()
			}
			return fmt.Errorf("could not flush log output: %v", strings.Join(msgs, ", "))
		}
		return nil
	}

	// Run the command
	if err := e.processes.Exec(cmd); err != nil {
		// close off our outputs. We errored, so we mostly don't care if we fail to close
		_ = closeOutputs()
		// if we already know we're in the process of exiting,
		// we don't need to record an error to that effect.
		if errors.Is(err, process.ErrClosing) {
			return nil
		}
		tracer(TargetBuildFailed, err)
		targetLogger.Error("Error: command finished with error: %w", err)
		if !e.rs.Opts.runOpts.continueOnError {
			targetUi.Error(fmt.Sprintf("ERROR: command finished with error: %s", err))
			e.processes.Close()
		} else {
			targetUi.Warn("command finished with error, but continuing...")
		}
		return err
	}

	duration := time.Since(cmdTime)
	// Close off our outputs and cache them
	if err := closeOutputs(); err != nil {
		e.logError(targetLogger, "", err)
	} else {
		if err = taskCache.SaveOutputs(targetLogger, targetUi, int(duration.Milliseconds())); err != nil {
			e.logError(targetLogger, "", fmt.Errorf("error caching output: %w", err))
		}
	}

	// Clean up tracing
	tracer(TargetBuilt, nil)
	targetLogger.Debug("done", "status", "complete", "duration", duration)
	return nil
}

func (r *run) generateDotGraph(taskGraph *dag.AcyclicGraph, outputFilename fs.AbsolutePath) error {
	graphString := string(taskGraph.Dot(&dag.DotOpts{
		Verbose:    true,
		DrawCycles: true,
	}))
	ext := outputFilename.Ext()
	if ext == ".html" {
		f, err := outputFilename.Create()
		if err != nil {
			return fmt.Errorf("error writing graph: %w", err)
		}
		defer f.Close()
		f.WriteString(`<!DOCTYPE html>
    <html>
    <head>
      <meta charset="utf-8">
      <title>Graph</title>
    </head>
    <body>
      <script src="https://cdn.jsdelivr.net/npm/viz.js@2.1.2-pre.1/viz.js"></script>
      <script src="https://cdn.jsdelivr.net/npm/viz.js@2.1.2-pre.1/full.render.js"></script>
      <script>`)
		f.WriteString("const s = `" + graphString + "`.replace(/\\_\\_\\_ROOT\\_\\_\\_/g, \"Root\").replace(/\\[root\\]/g, \"\");new Viz().renderSVGElement(s).then(el => document.body.appendChild(el)).catch(e => console.error(e));")
		f.WriteString(`
    </script>
  </body>
  </html>`)
		r.ui.Output("")
		r.ui.Output(fmt.Sprintf("✔ Generated task graph in %s", ui.Bold(outputFilename.ToString())))
		if ui.IsTTY {
			if err := browser.OpenBrowser(outputFilename.ToString()); err != nil {
				r.ui.Warn(color.New(color.FgYellow, color.Bold, color.ReverseVideo).Sprintf("failed to open browser. Please navigate to file://%v", filepath.ToSlash(outputFilename.ToString())))
			}
		}
		return nil
	}
	hasDot := hasGraphViz()
	if hasDot {
		dotArgs := []string{"-T" + ext[1:], "-o", outputFilename.ToString()}
		cmd := exec.Command("dot", dotArgs...)
		cmd.Stdin = strings.NewReader(graphString)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("could not generate task graphfile %v:  %w", outputFilename, err)
		} else {
			r.ui.Output("")
			r.ui.Output(fmt.Sprintf("✔ Generated task graph in %s", ui.Bold(outputFilename.ToString())))
		}
	} else {
		r.ui.Output("")
		r.ui.Warn(color.New(color.FgYellow, color.Bold, color.ReverseVideo).Sprint(" WARNING ") + color.YellowString(" `turbo` uses Graphviz to generate an image of your\ngraph, but Graphviz isn't installed on this machine.\n\nYou can download Graphviz from https://graphviz.org/download.\n\nIn the meantime, you can use this string output with an\nonline Dot graph viewer."))
		r.ui.Output("")
		r.ui.Output(graphString)
	}
	return nil
}

func (g *completeGraph) getPackageTaskVisitor(visitor func(pt *nodes.PackageTask) error) func(taskID string) error {
	return func(taskID string) error {
		name, task := util.GetPackageTaskFromId(taskID)
		pkg, ok := g.PackageInfos[name]
		if !ok {
			return fmt.Errorf("cannot find package %v for task %v", name, taskID)
		}
		// first check for package-tasks
		pipeline, ok := g.Pipeline[fmt.Sprintf("%v", taskID)]
		if !ok {
			// then check for regular tasks
			altpipe, notcool := g.Pipeline[task]
			// if neither, then bail
			if !notcool && !ok {
				return nil
			}
			// override if we need to...
			pipeline = altpipe
		}
		return visitor(&nodes.PackageTask{
			TaskID:         taskID,
			Task:           task,
			PackageName:    name,
			Pkg:            pkg,
			TaskDefinition: &pipeline,
		})
	}
}

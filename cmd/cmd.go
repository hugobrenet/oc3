package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/opensvc/oc3/util/logkey"
	"github.com/opensvc/oc3/util/version"
)

var (
	debug     bool
	logCaller bool

	GroupIDSubsystems = "subsystems"
)

const (
	sectionFeeder    = "feeder"
	sectionMessenger = "messenger"
	sectionRunner    = "runner"
	sectionScheduler = "scheduler"
	sectionServer    = "server"
	sectionWorker    = "worker"
)

func NewGroupSubsystems() *cobra.Group {
	return &cobra.Group{
		ID:    GroupIDSubsystems,
		Title: "Subsystems:",
	}
}

func cmdWorker() *cobra.Command {
	var maxRunners int
	var name string
	var queues []string
	cmd := &cobra.Command{
		GroupID: GroupIDSubsystems,
		Use:     "worker",
		Short:   "run queued jobs",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.Contains(name, ".") {
				return fmt.Errorf("unexpected worker name with '.': %s", name)
			}
			setDefaultWorkerConfig(name)
			section := workerSection(name)
			if err := setup(section); err != nil {
				return err
			} else if t, err := newWorker(section, maxRunners, queues); err != nil {
				return err
			} else {
				return t.run()
			}
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "worker name")
	cmd.Flags().IntVar(&maxRunners, "runners", 1, "maximum number of worker job runners")
	cmd.Flags().StringSliceVar(&queues, "queues", []string{}, "worker queue to listen to")
	return cmd
}

func cmdFeeder() *cobra.Command {
	return &cobra.Command{
		GroupID: GroupIDSubsystems,
		Use:     "feeder",
		Short:   "serve the data feed api to nodes",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := setup(sectionFeeder); err != nil {
				return err
			}
			if t, err := newFeeder(); err != nil {
				return err
			} else {
				return run(t)
			}
		},
	}
}

func cmdApiCollector() *cobra.Command {
	return &cobra.Command{
		GroupID: GroupIDSubsystems,
		Use:     "server",
		Short:   "serve the collector api to clients",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := setup(sectionServer); err != nil {
				return err
			}
			if t, err := newServer(); err != nil {
				return err
			} else {
				return run(t)
			}
		},
	}
}

func cmdScheduler() *cobra.Command {
	return &cobra.Command{
		GroupID: GroupIDSubsystems,
		Use:     "scheduler",
		Short:   "start running db maintenance tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			return startScheduler()
		},
	}
}

func cmdRunner() *cobra.Command {
	return &cobra.Command{
		GroupID: GroupIDSubsystems,
		Use:     "runner",
		Short:   "dispatch actions to nodes",
		RunE: func(cmd *cobra.Command, args []string) error {
			return startRunner()
		},
	}
}

func cmdMessenger() *cobra.Command {
	return &cobra.Command{
		GroupID: GroupIDSubsystems,
		Use:     "messenger",
		Short:   "notify clients of data changes",
		RunE: func(cmd *cobra.Command, args []string) error {
			return startMessenger()
		},
	}
}

func cmdSchedulerList() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list the tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			return scheduleList()
		},
	}
}

func cmdSchedulerExec() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "exec",
		Short: "execute a task inline",
		RunE: func(cmd *cobra.Command, args []string) error {
			return scheduleExec(name)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "the task name")
	return cmd
}

func cmdVersion() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "display the oc3 version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version.Version())
		},
	}
	return cmd
}

func Root(args []string) *cobra.Command {
	cmd := &cobra.Command{
		Use:          filepath.Base(args[0]),
		Short:        "Manage the opensvc collector infrastructure components.",
		SilenceUsage: true,
	}
	cmd.AddGroup(NewGroupSubsystems())
	cmd.PersistentFlags().BoolVar(&debug, "debug", false, "set log level to debug")
	cmd.PersistentFlags().BoolVar(&logCaller, "caller", false, "log source code location")
	grpScheduler := cmdScheduler()
	grpScheduler.AddCommand(
		cmdSchedulerExec(),
		cmdSchedulerList(),
	)
	cmd.AddCommand(
		cmdFeeder(),
		cmdApiCollector(),
		grpScheduler,
		cmdVersion(),
		cmdWorker(),
		cmdRunner(),
		cmdMessenger(),
		cmdSafe(),
		cmdDB(),
	)
	return cmd
}

func setup(section string) error {
	// Prepare default logging
	logLevel := slog.LevelInfo
	if debug {
		logLevel = slog.LevelDebug
		slog.SetLogLoggerLevel(logLevel)
	}
	logHandlerOptions := slog.HandlerOptions{Level: logLevel}
	if logCaller {
		logHandlerOptions.AddSource = true
	}
	// The db commands print what they report, SQL or a status table, on stdout,
	// to be read or piped: their logs go to stderr.
	logOut := os.Stdout
	if section == sectionDB {
		logOut = os.Stderr
	}
	logHandler := slog.NewJSONHandler(logOut, &logHandlerOptions)
	slog.SetDefault(slog.New(logHandler).With(slog.String(logkey.Section, section)))
	slog.Debug(fmt.Sprintf("candidate config directories: %s", configCandidateDirs))

	if err := initConfig(); err != nil {
		return err
	}
	setDefaultNotificationsConfig()
	slog.Info(fmt.Sprintf("used config file: %s", viper.ConfigFileUsed()))
	slog.Info(fmt.Sprintf("oc3 version: %s", version.Version()))
	return nil
}

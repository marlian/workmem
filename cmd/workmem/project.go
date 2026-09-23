package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"workmem/internal/mcpserver"
	"workmem/internal/store"
)

// configureProjectStore installs this instance's project storage policy from
// the environment, using the global DB path (flag, MEMORY_DB_PATH, or the
// binary-relative default) to derive the default central root.
func configureProjectStore(dbFlag string) error {
	globalDB, err := mcpserver.ResolveDBPath(dbFlag)
	if err != nil {
		return fmt.Errorf("resolve global db: %w", err)
	}
	return store.ConfigureProjectStoreFromEnv("", globalDB)
}

// cliProjectPath resolves a project path given on the command line. Unlike
// the MCP `project` argument (home-relative for relative paths), CLI paths
// follow shell conventions: relative paths resolve from the working directory
// and `~` is expanded by store.ResolveProjectPath.
func cliProjectPath(path string) (string, error) {
	if path == "" || path == "~" || strings.HasPrefix(path, "~/") || filepath.IsAbs(path) {
		return path, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", path, err)
	}
	return abs, nil
}

func runProject(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "project: missing subcommand (list, import, move)")
		printUsage()
		os.Exit(2)
	}
	var err error
	switch args[0] {
	case "list":
		err = runProjectList(args[1:])
	case "import":
		err = runProjectImport(args[1:])
	case "move":
		err = runProjectMove(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "project: unknown subcommand %q\n\n", args[0])
		printUsage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "project %s: %v\n", args[0], err)
		os.Exit(1)
	}
}

func newProjectFlagSet(name string) (*flag.FlagSet, *string, *string) {
	fs := flag.NewFlagSet("project "+name, flag.ExitOnError)
	dbPath := fs.String("db", "", "global DB path used to derive the default projects root")
	envFile := fs.String("env-file", "", "path to a .env file to load before running (process env wins over file values); a missing or unreadable file is an error")
	return fs, dbPath, envFile
}

// centralStoreConfig loads the environment and requires central mode. Like
// serve, a missing explicit -env-file is fatal: import and move create or
// rewrite registry state, and a silent fallback would do it under the wrong
// root (DECISION_LOG 2026-09-23).
func centralStoreConfig(dbPath string, envFile string) (store.ProjectStoreConfig, error) {
	if err := loadRequiredEnvFile(envFile); err != nil {
		return store.ProjectStoreConfig{}, err
	}
	if err := configureProjectStore(dbPath); err != nil {
		return store.ProjectStoreConfig{}, err
	}
	config := store.CurrentProjectStore()
	if config.Mode != store.ProjectModeCentral {
		return store.ProjectStoreConfig{}, fmt.Errorf("requires MEMORY_PROJECT_MODE=central (current: %s)", config.Mode)
	}
	return config, nil
}

func runProjectList(args []string) error {
	fs, dbPath, envFile := newProjectFlagSet("list")
	_ = fs.Parse(args)
	config, err := centralStoreConfig(*dbPath, *envFile)
	if err != nil {
		return err
	}
	registry, err := store.OpenProjectRegistry(config.Root)
	if err != nil {
		return err
	}
	defer registry.Close()
	records, err := store.ListProjects(registry)
	if err != nil {
		return err
	}
	fmt.Printf("root: %s\n", config.Root)
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tPATH\tPATH_EXISTS\tDB_BYTES")
	for _, record := range records {
		pathExists := "yes"
		if info, err := os.Stat(record.CanonicalPath); err != nil || !info.IsDir() {
			pathExists = "no"
		}
		size := "-"
		if info, err := os.Stat(store.ProjectDBPath(config.Root, record.ID)); err == nil {
			size = fmt.Sprint(info.Size())
		} else if record.Initialized {
			size = "MISSING"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", record.ID, record.CanonicalPath, pathExists, size)
	}
	return w.Flush()
}

func runProjectImport(args []string) error {
	fs, dbPath, envFile := newProjectFlagSet("import")
	from := fs.String("from", "", "existing memory DB to copy (never modified or removed)")
	projectPath := fs.String("path", "", "project directory to register the copy for")
	_ = fs.Parse(args)
	if *from == "" || *projectPath == "" {
		return fmt.Errorf("--from and --path are required")
	}
	config, err := centralStoreConfig(*dbPath, *envFile)
	if err != nil {
		return err
	}
	target, err := cliProjectPath(*projectPath)
	if err != nil {
		return err
	}
	record, err := store.ImportProject(config.Root, *from, target)
	if err != nil {
		return err
	}
	fmt.Printf("project import: %s -> %s (%s)\n", *from, record.CanonicalPath, record.ID)
	if store.LegacyProjectDBExists(record.CanonicalPath) {
		fmt.Printf("project import: note: %s still exists; central mode refuses this project until that legacy .memory directory is moved out\n", store.LegacyProjectDBPath(record.CanonicalPath))
	}
	return nil
}

func runProjectMove(args []string) error {
	fs, dbPath, envFile := newProjectFlagSet("move")
	replaceEmpty := fs.Bool("replace-empty", false, "archive an empty store already registered at the new path (created by using it before the move)")
	_ = fs.Parse(args)
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: workmem project move [flags] <old-path> <new-path>")
	}
	config, err := centralStoreConfig(*dbPath, *envFile)
	if err != nil {
		return err
	}
	oldPath, err := cliProjectPath(fs.Arg(0))
	if err != nil {
		return err
	}
	newPath, err := cliProjectPath(fs.Arg(1))
	if err != nil {
		return err
	}
	result, err := store.MoveProject(config.Root, oldPath, newPath, *replaceEmpty)
	if err != nil {
		return err
	}
	if result.DiscardedID != "" {
		fmt.Printf("project move: archived empty store %s under %s\n", result.DiscardedID, filepath.Join(config.Root, "discarded"))
	}
	fmt.Printf("project move: %s now at %s\n", result.Record.ID, result.Record.CanonicalPath)
	return nil
}

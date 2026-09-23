package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
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
	config, err := store.ProjectStoreConfigFromEnv(globalDB)
	if err != nil {
		return err
	}
	return store.ConfigureProjectStore(config)
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
	envFile := fs.String("env-file", "", "path to a .env file to load before running (process env wins over file values)")
	return fs, dbPath, envFile
}

// openCentralRegistry loads the environment, requires central mode, and opens
// the registry of the configured root.
func openCentralRegistry(dbPath string, envFile string) (store.ProjectStoreConfig, *sql.DB, error) {
	loadEnvFile(envFile)
	if err := configureProjectStore(dbPath); err != nil {
		return store.ProjectStoreConfig{}, nil, err
	}
	config := store.CurrentProjectStore()
	if config.Mode != store.ProjectModeCentral {
		return store.ProjectStoreConfig{}, nil, fmt.Errorf("requires MEMORY_PROJECT_MODE=central (current: %s)", config.Mode)
	}
	registry, err := store.OpenProjectRegistry(config.Root)
	if err != nil {
		return store.ProjectStoreConfig{}, nil, err
	}
	return config, registry, nil
}

func runProjectList(args []string) error {
	fs, dbPath, envFile := newProjectFlagSet("list")
	_ = fs.Parse(args)
	config, registry, err := openCentralRegistry(*dbPath, *envFile)
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
	config, registry, err := openCentralRegistry(*dbPath, *envFile)
	if err != nil {
		return err
	}
	registry.Close()
	record, err := store.ImportProject(config.Root, *from, *projectPath)
	if err != nil {
		return err
	}
	fmt.Printf("project import: %s -> %s (%s)\n", *from, record.CanonicalPath, record.ID)
	return nil
}

func runProjectMove(args []string) error {
	fs, dbPath, envFile := newProjectFlagSet("move")
	_ = fs.Parse(args)
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: workmem project move [flags] <old-path> <new-path>")
	}
	_, registry, err := openCentralRegistry(*dbPath, *envFile)
	if err != nil {
		return err
	}
	defer registry.Close()
	record, err := store.MoveProject(registry, fs.Arg(0), fs.Arg(1))
	if err != nil {
		return err
	}
	fmt.Printf("project move: %s now at %s\n", record.ID, record.CanonicalPath)
	return nil
}

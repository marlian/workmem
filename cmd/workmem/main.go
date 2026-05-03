package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"workmem/internal/backup"
	"workmem/internal/dotenv"
	"workmem/internal/mcpserver"
	"workmem/internal/store"
	"workmem/internal/telemetry"
)

// Build metadata overridden at link time via -ldflags "-X main.version=..."
// in the release workflow. Defaults keep `go build` and `go run` usable
// without extra flags and clearly flag unreleased binaries as dev builds.
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

func main() {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			printVersion()
			return
		}
	}

	if len(os.Args) < 2 {
		runMCP(nil)
		return
	}

	switch {
	case os.Args[1] == "serve":
		runMCP(os.Args[2:])
	case os.Args[1] == "sqlite-canary":
		runSQLiteCanary(os.Args[2:])
	case os.Args[1] == "backup":
		runBackup(os.Args[2:])
	case os.Args[1] == "reconcile":
		runReconcile(os.Args[2:])
	case os.Args[1][0] == '-':
		// no subcommand, treat remaining args as flags for the default (serve) command
		runMCP(os.Args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(2)
	}
}

func printVersion() {
	fmt.Printf("workmem %s\n", version)
	fmt.Printf("  commit: %s\n", commit)
	fmt.Printf("  built:  %s\n", buildDate)
	fmt.Printf("  go:     %s\n", runtime.Version())
}

// recipientFlag collects repeatable --age-recipient arguments.
type recipientFlag []string

func (r *recipientFlag) String() string { return fmt.Sprint([]string(*r)) }
func (r *recipientFlag) Set(value string) error {
	*r = append(*r, value)
	return nil
}

func runBackup(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dbPath := fs.String("db", "", "path to the SQLite database file (defaults to MEMORY_DB_PATH or binary-relative memory.db)")
	envFile := fs.String("env-file", "", "path to a .env file to load before running (process env wins over file values)")
	to := fs.String("to", "", "destination file for the encrypted snapshot (required)")
	var recipients recipientFlag
	fs.Var(&recipients, "age-recipient", "age recipient public key (age1...) or path to a recipients file; repeatable, at least one required")
	_ = fs.Parse(args)

	loadEnvFile(*envFile)

	if *to == "" {
		fmt.Fprintln(os.Stderr, "backup: --to <path> is required")
		os.Exit(2)
	}
	if len(recipients) == 0 {
		fmt.Fprintln(os.Stderr, "backup: at least one --age-recipient is required")
		os.Exit(2)
	}

	sourceDB, err := mcpserver.ResolveDBPath(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "backup: resolve source db: %v\n", err)
		os.Exit(1)
	}

	parsed, err := backup.ParseRecipients(recipients)
	if err != nil {
		fmt.Fprintf(os.Stderr, "backup: %v\n", err)
		os.Exit(1)
	}

	// Signal-aware context so Ctrl+C (SIGINT) and SIGTERM can interrupt a
	// long VACUUM cleanly. The backup package threads the context into
	// sql.ExecContext, so cancellation propagates to the SQLite driver.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := backup.Run(ctx, sourceDB, *to, parsed); err != nil {
		fmt.Fprintf(os.Stderr, "backup: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("backup: wrote encrypted snapshot of %s to %s (%d recipient(s))\n", sourceDB, *to, len(parsed))
}

func runMCP(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dbPath := fs.String("db", "", "path to the SQLite database file")
	envFile := fs.String("env-file", "", "path to a .env file to load before starting (process env wins over file values)")
	// flag.ExitOnError calls os.Exit on parse failure — no need to check err.
	_ = fs.Parse(args)

	loadEnvFile(*envFile)

	// Ownership of the telemetry client transfers to the Runtime only after
	// New returns successfully. If New fails, the DB was already opened by
	// FromEnv and must be closed here — otherwise the handle leaks.
	tele := telemetry.FromEnv()
	rt, err := mcpserver.New(mcpserver.Config{
		DBPath:    *dbPath,
		Telemetry: tele,
	})
	if err != nil {
		_ = tele.Close() // nil-safe no-op when telemetry is disabled
		fmt.Fprintf(os.Stderr, "start mcp server: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := rt.RunStdio(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "mcp server failed: %v\n", err)
		os.Exit(1)
	}
}

func runSQLiteCanary(args []string) {
	fs := flag.NewFlagSet("sqlite-canary", flag.ExitOnError)
	dbPath := fs.String("db", "", "path to the SQLite database file")
	envFile := fs.String("env-file", "", "path to a .env file to load before starting (process env wins over file values)")
	// flag.ExitOnError calls os.Exit on parse failure — no need to check err.
	_ = fs.Parse(args)

	loadEnvFile(*envFile)

	result, err := store.RunSQLiteCanary(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sqlite canary failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("sqlite canary ok\n")
	fmt.Printf("driver=%s\n", result.Driver)
	fmt.Printf("database=%s\n", result.DatabasePath)
	fmt.Printf("observation_id=%d\n", result.ObservationID)
	fmt.Printf("fts_before_delete=%d\n", result.MatchCountBeforeDelete)
	fmt.Printf("fts_after_delete=%d\n", result.MatchCountAfterDelete)
	fmt.Printf("foreign_keys_enabled=%t\n", result.ForeignKeysEnabled)
	fmt.Printf("orphan_insert_rejected=%t\n", result.OrphanInsertRejected)
	fmt.Printf("persisted_observations=%d\n", result.PersistedObservationCount)
}

// loadEnvFile loads the given .env file if non-empty. Errors are logged to
// stderr and the process continues with whatever environment it already has —
// a failed .env load must not prevent the server from starting.
func loadEnvFile(path string) {
	if path == "" {
		return
	}
	if err := dotenv.Load(path); err != nil {
		fmt.Fprintf(os.Stderr, "[workmem] warning: cannot load env-file: %v\n", err)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "usage: workmem [serve] [flags]\n")
	fmt.Fprintf(os.Stderr, "       workmem <command> [flags]\n\n")
	fmt.Fprintf(os.Stderr, "commands:\n")
	fmt.Fprintf(os.Stderr, "  serve           run the MCP server over stdio (default)\n")
	fmt.Fprintf(os.Stderr, "  sqlite-canary   prove schema init, FTS insert/match/delete, and persistence\n")
	fmt.Fprintf(os.Stderr, "  backup          write an age-encrypted snapshot of memory.db\n")
	fmt.Fprintf(os.Stderr, "  reconcile       propose/apply deterministic memory hygiene candidates\n")
	fmt.Fprintf(os.Stderr, "  version         print build metadata (also: --version / -v)\n\n")
	fmt.Fprintf(os.Stderr, "flags (serve, sqlite-canary, backup, exact reconcile, rollback):\n")
	fmt.Fprintf(os.Stderr, "  -db <path>        path to the SQLite database file\n")
	fmt.Fprintf(os.Stderr, "  -env-file <path>  load variables from a .env file (process env takes precedence)\n\n")
	fmt.Fprintf(os.Stderr, "backup flags:\n")
	fmt.Fprintf(os.Stderr, "  -to <path>            destination file for the encrypted snapshot (required)\n")
	fmt.Fprintf(os.Stderr, "  -age-recipient <key>  age recipient (age1... or file path), repeatable, at least one required\n\n")
	fmt.Fprintf(os.Stderr, "reconcile flags:\n")
	fmt.Fprintf(os.Stderr, "  -mode propose|apply        report or apply exact duplicate supersession\n")
	fmt.Fprintf(os.Stderr, "  -scope global|project=PATH scan global or project memory\n")
	fmt.Fprintf(os.Stderr, "  -since <duration>          scan entities with recent observations (default: 30d)\n")
	fmt.Fprintf(os.Stderr, "  -min-obs-per-entity <n>    minimum active observations per scanned entity (default: 2)\n")
	fmt.Fprintf(os.Stderr, "  -max-entities-per-run <n>  maximum entities to scan (default: 50)\n")
	fmt.Fprintf(os.Stderr, "  -output <path>             markdown report path (default: review/reconcile-<timestamp>.md)\n\n")
	fmt.Fprintf(os.Stderr, "reconcile rollback:\n")
	fmt.Fprintf(os.Stderr, "  workmem reconcile rollback [flags] <run_id>  (use the same --scope as the apply run)\n\n")
	fmt.Fprintf(os.Stderr, "reconcile semantic:\n")
	fmt.Fprintf(os.Stderr, "  workmem reconcile semantic [flags]  validate semantic provider config only; propose/apply not implemented\n")
	fmt.Fprintf(os.Stderr, "  no -db or -scope flag: this command opens no memory database\n")
	fmt.Fprintf(os.Stderr, "  -embedding-provider none|openai-compatible|ollama|openai\n")
	fmt.Fprintf(os.Stderr, "  -embedding-base-url <url>       required for non-none providers\n")
	fmt.Fprintf(os.Stderr, "  -embedding-model <id>           required for non-none providers\n")
	fmt.Fprintf(os.Stderr, "  -embedding-dimensions <n>       required for non-none providers\n")
	fmt.Fprintf(os.Stderr, "  -allow-remote-embeddings        required for non-loopback endpoints and openai\n\n")
	fmt.Fprintf(os.Stderr, "restore a backup with the age CLI:\n")
	fmt.Fprintf(os.Stderr, "  age -d -i <identity-file> <backup.age> > memory.db\n")
}

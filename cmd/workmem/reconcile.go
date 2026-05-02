package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"workmem/internal/embedding"
	"workmem/internal/mcpserver"
	"workmem/internal/reconcile"
	"workmem/internal/store"
)

func runReconcile(args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "rollback":
			runReconcileRollback(args[1:])
			return
		case "semantic":
			runReconcileSemantic(args[1:])
			return
		}
	}

	fs := flag.NewFlagSet("reconcile", flag.ExitOnError)
	dbPath := fs.String("db", "", "path to the SQLite database file for global scope")
	envFile := fs.String("env-file", "", "path to a .env file to load before running (process env wins over file values)")
	mode := fs.String("mode", "propose", "reconcile mode: propose or apply")
	scope := fs.String("scope", "global", "scan scope: global or project=<path>")
	sinceRaw := fs.String("since", "30d", "scan window duration, e.g. 30d or 720h")
	minObsPerEntity := fs.Int("min-obs-per-entity", 2, "minimum active observations per scanned entity")
	maxEntitiesPerRun := fs.Int("max-entities-per-run", 50, "maximum entities to scan")
	output := fs.String("output", "", "markdown report path (default: review/reconcile-<timestamp>.md)")
	_ = fs.Parse(args)

	loadEnvFile(*envFile)
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "reconcile: unexpected positional argument(s): %s\n", strings.Join(fs.Args(), " "))
		os.Exit(2)
	}

	if *mode != "propose" && *mode != "apply" {
		fmt.Fprintf(os.Stderr, "reconcile: unsupported --mode %q (use propose or apply)\n", *mode)
		os.Exit(2)
	}
	if *mode == "apply" && strings.TrimSpace(*output) != "" {
		fmt.Fprintln(os.Stderr, "reconcile: --output is only valid with --mode propose")
		os.Exit(2)
	}
	if *minObsPerEntity <= 0 {
		fmt.Fprintln(os.Stderr, "reconcile: --min-obs-per-entity must be > 0")
		os.Exit(2)
	}
	if *maxEntitiesPerRun <= 0 {
		fmt.Fprintln(os.Stderr, "reconcile: --max-entities-per-run must be > 0")
		os.Exit(2)
	}
	since, err := parseReconcileSince(*sinceRaw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: invalid --since: %v\n", err)
		os.Exit(2)
	}

	db, release, scopeLabel, err := openReconcileDB(*scope, *dbPath, *mode == "propose")
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: %v\n", err)
		os.Exit(1)
	}
	defer release()

	if *mode == "apply" {
		result, err := store.ApplyExactDuplicateReconcile(db, store.ReconcileApplyOptions{
			GeneratedAt:       time.Now().UTC(),
			Since:             since,
			SinceLabel:        *sinceRaw,
			MinObsPerEntity:   *minObsPerEntity,
			MaxEntitiesPerRun: *maxEntitiesPerRun,
			Scope:             scopeLabel,
			TriggerSource:     "cli",
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "reconcile: apply exact duplicates: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("reconcile apply: run %d applied %d supersession(s) across %d decision(s) (%d candidate(s), %d scanned entit(y/ies))\n",
			result.RunID,
			result.SupersessionsApplied,
			result.DecisionsRecorded,
			result.CandidatesProposed,
			result.ScannedEntities,
		)
		return
	}

	report, err := store.BuildReconcileProposeReport(db, store.ReconcileProposeOptions{
		GeneratedAt:       time.Now().UTC(),
		Since:             since,
		SinceLabel:        *sinceRaw,
		MinObsPerEntity:   *minObsPerEntity,
		MaxEntitiesPerRun: *maxEntitiesPerRun,
		Scope:             scopeLabel,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: build propose report: %v\n", err)
		os.Exit(1)
	}
	reportPath, err := reconcile.WriteProposeReport(*output, report)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("reconcile propose: wrote %s (%d exact duplicate group(s), %d candidate(s), 0 mutation(s))\n",
		reportPath,
		len(report.DuplicateGroups),
		report.CandidatesProposed,
	)
}

func runReconcileSemantic(args []string) {
	fs := flag.NewFlagSet("reconcile semantic", flag.ExitOnError)
	envFile := fs.String("env-file", "", "path to a .env file to load before running (process env wins over file values)")
	mode := fs.String("mode", "propose", "semantic reconcile mode: propose only")
	provider := fs.String("embedding-provider", "", "embedding provider: none, openai-compatible, ollama, or openai")
	baseURL := fs.String("embedding-base-url", "", "embedding provider base URL")
	model := fs.String("embedding-model", "", "embedding model identifier")
	dimensions := fs.Int("embedding-dimensions", 0, "embedding vector dimensions")
	allowRemote := fs.Bool("allow-remote-embeddings", false, "allow non-loopback embedding endpoints or the openai provider")
	_ = fs.Parse(args)

	loadEnvFile(*envFile)
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "reconcile semantic: unexpected positional argument(s): %s\n", strings.Join(fs.Args(), " "))
		os.Exit(2)
	}
	if *mode != "propose" {
		fmt.Fprintf(os.Stderr, "reconcile semantic: unsupported --mode %q (semantic reconcile is propose-only)\n", *mode)
		os.Exit(2)
	}

	options, err := embedding.OptionsFromEnv(os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile semantic: %v\n", err)
		os.Exit(2)
	}
	if flagWasSet(fs, "embedding-provider") {
		options.Provider = *provider
	}
	if flagWasSet(fs, "embedding-base-url") {
		options.BaseURL = *baseURL
	}
	if flagWasSet(fs, "embedding-model") {
		options.Model = *model
	}
	if flagWasSet(fs, "embedding-dimensions") {
		options.Dimensions = *dimensions
	}
	if flagWasSet(fs, "allow-remote-embeddings") {
		options.AllowRemote = *allowRemote
	}
	cfg, err := embedding.ParseConfig(options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile semantic: %v\n", err)
		os.Exit(2)
	}
	if cfg.Provider == embedding.ProviderNone {
		fmt.Println("reconcile semantic: provider=none; semantic candidate generation is not implemented yet (0 network call(s), 0 mutation(s))")
		return
	}
	fmt.Printf("reconcile semantic: provider=%s model=%s dimensions=%d validated; semantic candidate generation is not implemented yet (0 network call(s), 0 mutation(s))\n",
		cfg.Provider,
		cfg.Model,
		cfg.Dimensions,
	)
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(flag *flag.Flag) {
		if flag.Name == name {
			found = true
		}
	})
	return found
}

func runReconcileRollback(args []string) {
	fs := flag.NewFlagSet("reconcile rollback", flag.ExitOnError)
	dbPath := fs.String("db", "", "path to the SQLite database file for global scope")
	envFile := fs.String("env-file", "", "path to a .env file to load before running (process env wins over file values)")
	scope := fs.String("scope", "global", "scan scope: global or project=<path>")
	_ = fs.Parse(args)

	loadEnvFile(*envFile)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "reconcile rollback: expected exactly one run_id")
		os.Exit(2)
	}
	runID, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil || runID <= 0 {
		fmt.Fprintf(os.Stderr, "reconcile rollback: invalid run_id %q\n", fs.Arg(0))
		os.Exit(2)
	}
	db, release, scopeLabel, err := openReconcileDB(*scope, *dbPath, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile rollback: %v\n", err)
		os.Exit(1)
	}
	defer release()
	result, err := store.RollbackReconcileRun(db, store.ReconcileRollbackOptions{
		RunID:         runID,
		Scope:         scopeLabel,
		TriggerSource: "cli",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile rollback: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("reconcile rollback: run %d restored %d supersession(s) across %d decision(s) from run %d\n",
		result.RunID,
		result.SupersessionsRestored,
		result.DecisionsReverted,
		result.RolledBackRunID,
	)
}

func openReconcileDB(scopeValue string, dbPath string, readOnly bool) (*sql.DB, func(), string, error) {
	scopeValue = strings.TrimSpace(scopeValue)
	if scopeValue == "" || scopeValue == "global" {
		resolved, err := mcpserver.ResolveDBPath(dbPath)
		if err != nil {
			return nil, nil, "", fmt.Errorf("resolve global db: %w", err)
		}
		open := store.OpenExistingDB
		openLabel := "read-write"
		if readOnly {
			open = store.OpenReadOnlyDB
			openLabel = "read-only"
		}
		db, err := open(resolved)
		if err != nil {
			return nil, nil, "", fmt.Errorf("open global db %s: %w", openLabel, err)
		}
		return db, func() { _ = db.Close() }, "global", nil
	}
	const projectPrefix = "project="
	if !strings.HasPrefix(scopeValue, projectPrefix) {
		return nil, nil, "", fmt.Errorf("invalid --scope %q (use global or project=<path>)", scopeValue)
	}
	project := strings.TrimSpace(strings.TrimPrefix(scopeValue, projectPrefix))
	if project == "" {
		return nil, nil, "", fmt.Errorf("invalid --scope %q: project path is empty", scopeValue)
	}
	if strings.TrimSpace(dbPath) != "" {
		return nil, nil, "", fmt.Errorf("--db is only valid with --scope global")
	}
	resolved, projectDBPath := store.ResolveProjectDBPath(project, "")
	resolved = filepath.Clean(resolved)
	open := store.OpenExistingDB
	openLabel := "read-write"
	if readOnly {
		open = store.OpenReadOnlyDB
		openLabel = "read-only"
	}
	db, err := open(projectDBPath)
	if err != nil {
		return nil, nil, "", fmt.Errorf("open project db %s: %w", openLabel, err)
	}
	return db, func() { _ = db.Close() }, "project:" + resolved, nil
}

func parseReconcileSince(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if strings.HasSuffix(value, "d") {
		daysText := strings.TrimSuffix(value, "d")
		days, err := time.ParseDuration(daysText + "h")
		if err != nil {
			return 0, fmt.Errorf("parse day count %q: %w", daysText, err)
		}
		duration := days * 24
		if duration <= 0 {
			return 0, fmt.Errorf("duration must be > 0")
		}
		return duration, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, err
	}
	if duration <= 0 {
		return 0, fmt.Errorf("duration must be > 0")
	}
	return duration, nil
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"time"

	"homebox/internal/store"
)

const authUsage = `Usage:
  homebox auth bootstrap --output PRIVATE_FILE [--database PATH] [--timeout DURATION]

Stop the server first. The database must already be migrated.
The new output file and its directory must be private; existing paths are never overwritten.
Only file metadata is printed. The setup secret is stored in the requested file, not stdout.
`

func authCommand(ctx context.Context, args []string, stdout, stderr io.Writer) (finalErr error) {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h")) {
		_, err := io.WriteString(stdout, authUsage)
		return err
	}
	if args[0] != "bootstrap" {
		return errors.New("unknown authentication command; run 'homebox auth --help'")
	}
	flags := flag.NewFlagSet("homebox auth bootstrap", flag.ContinueOnError)
	flags.SetOutput(stderr)
	database := flags.String("database", "data/homebox.sqlite", "Previously migrated private SQLite database")
	output := flags.String("output", "", "New private setup-secret file; never an existing file")
	timeout := flags.Duration("timeout", 2*time.Minute, "Maximum operation duration")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments after authentication command")
	}
	if *output == "" {
		return errors.New("--output is required")
	}
	if *timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	db, err := store.Open(ctx, store.Config{Path: *database})
	if err != nil {
		return err
	}
	defer func() { finalErr = errors.Join(finalErr, db.Close()) }()
	auth, err := db.NewAuth(store.AuthConfig{})
	if err != nil {
		return err
	}
	delivery, err := auth.IssueBootstrapFile(ctx, *output)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(delivery)
}

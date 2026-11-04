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
  homebox auth reset-password --output PRIVATE_FILE --confirm RESET_PASSWORD
                              [--database PATH] [--timeout DURATION]

Stop the server first. The database must already be migrated.
The new output file and its directory must be private; existing paths are never overwritten.
Only file metadata is printed. Credentials are stored in the requested file, not stdout.
Password recovery revokes every session and requires a password change after the next login.
`

func authCommand(ctx context.Context, args []string, stdout, stderr io.Writer) (finalErr error) {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h")) {
		_, err := io.WriteString(stdout, authUsage)
		return err
	}
	verb := args[0]
	if verb != "bootstrap" && verb != "reset-password" {
		return errors.New("unknown authentication command; run 'homebox auth --help'")
	}
	flags := flag.NewFlagSet("homebox auth "+verb, flag.ContinueOnError)
	flags.SetOutput(stderr)
	database := flags.String("database", "data/homebox.sqlite", "Previously migrated private SQLite database")
	output := flags.String("output", "", "New private credential file; never an existing file")
	timeout := flags.Duration("timeout", 2*time.Minute, "Maximum operation duration")
	var confirmation string
	if verb == "reset-password" {
		flags.StringVar(&confirmation, "confirm", "", "Type RESET_PASSWORD to revoke sessions and replace the account credential")
	}
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
	if verb == "reset-password" && confirmation != "RESET_PASSWORD" {
		return errors.New("--confirm RESET_PASSWORD is required for offline credential recovery")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	db, err := store.Open(ctx, store.Config{Path: *database, OfflineAuth: verb == "reset-password"})
	if err != nil {
		return err
	}
	defer func() { finalErr = errors.Join(finalErr, db.Close()) }()
	auth, err := db.NewAuth(store.AuthConfig{})
	if err != nil {
		return err
	}
	if verb == "reset-password" {
		delivery, err := auth.RecoverPasswordFile(ctx, *output)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(delivery)
	}
	delivery, err := auth.IssueBootstrapFile(ctx, *output)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(delivery)
}

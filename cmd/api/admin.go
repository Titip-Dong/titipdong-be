package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/jackc/pgx/v5/pgxpool"
)

// createAdmin implements `api create-admin -name NAME -email EMAIL`, with the password in ADMIN_PASSWORD.
func createAdmin(ctx context.Context, pool *pgxpool.Pool, args []string) error {
	p, err := parseCreateAdmin(args, os.Getenv("ADMIN_PASSWORD"))
	if err != nil {
		return err
	}
	u, err := auth.Provision(ctx, pool, p)
	if errors.Is(err, auth.ErrEmailTaken) {
		return fmt.Errorf("create-admin: %s is already registered", p.Email)
	}
	if err != nil {
		return err
	}
	log.Printf("created admin %s (%s)", u.Email, u.ID)
	return nil
}

func parseCreateAdmin(args []string, password string) (auth.RegisterParams, error) {
	fs := flag.NewFlagSet("create-admin", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "display name")
	email := fs.String("email", "", "login email")
	if err := fs.Parse(args); err != nil {
		return auth.RegisterParams{}, fmt.Errorf("create-admin: %w", err)
	}
	switch {
	case *name == "" || *email == "":
		return auth.RegisterParams{}, errors.New("create-admin: -name and -email are required")
	case len(password) < 8 || len(password) > 128:
		return auth.RegisterParams{}, errors.New("create-admin: set ADMIN_PASSWORD to 8–128 characters")
	}
	return auth.RegisterParams{Name: *name, Email: *email, Password: password, Role: auth.RoleAdmin}, nil
}

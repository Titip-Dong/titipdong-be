package main

import (
	"context"
	"errors"
	"log"

	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/jackc/pgx/v5/pgxpool"
)

const demoPassword = "demo12345"

func seedDemo(ctx context.Context, pool *pgxpool.Pool) error {
	users := []auth.RegisterParams{
		{Name: "Jasmine Jastiper", Email: "jastiper@titipdong.test", Role: auth.RoleJastiper},
		{Name: "Budi Buyer", Email: "buyer@titipdong.test", Role: auth.RoleBuyer},
		{Name: "Admin", Email: "admin@titipdong.test", Role: auth.RoleAdmin},
	}
	for _, u := range users {
		u.Password = demoPassword
		_, err := auth.Provision(ctx, pool, u)
		switch {
		case errors.Is(err, auth.ErrEmailTaken):
			log.Printf("skip %s: already exists", u.Email)
		case err != nil:
			return err
		default:
			log.Printf("created %s (%s)", u.Email, u.Role)
		}
	}
	log.Printf("password for all demo accounts: %s", demoPassword)
	return nil
}

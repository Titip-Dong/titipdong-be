package main

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/Titip-Dong/titipdong-be/internal/auth"
	"github.com/Titip-Dong/titipdong-be/internal/trip"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const demoPassword = "demo12345"
const demoTripTitle = "Demo Tokyo Shopping Trip"

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
	if err := seedDemoTrip(ctx, pool); err != nil {
		return err
	}
	log.Printf("password for all demo accounts: %s", demoPassword)
	return nil
}

func seedDemoTrip(ctx context.Context, pool *pgxpool.Pool) error {
	trips := trip.NewService(pool, nil)
	var jastiperID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1 AND role = 'jastiper'`, "jastiper@titipdong.test").Scan(&jastiperID); err != nil {
		return err
	}

	var tripID uuid.UUID
	err := pool.QueryRow(ctx, `SELECT id FROM trips WHERE jastiper_id = $1 AND title = $2 ORDER BY created_at LIMIT 1`, jastiperID, demoTripTitle).Scan(&tripID)
	switch {
	case err == nil:
		log.Printf("skip demo trip %q: already exists", demoTripTitle)
	case errors.Is(err, pgx.ErrNoRows):
		now := time.Now().UTC()
		departsOn := time.Date(now.Year(), now.Month(), now.Day()+30, 0, 0, 0, 0, time.UTC)
		returnsOn := departsOn.AddDate(0, 0, 7)
		created, createErr := trips.Create(ctx, jastiperID, trip.CreateParams{
			Title:              demoTripTitle,
			Description:        "Contoh trip untuk mencoba alur TITIP DONG. Pilih barang dari katalog, ajukan pesanan, lalu ikuti proses sampai barang diterima.",
			DestinationCity:    "Tokyo",
			DestinationCountry: "JP",
			DepartsOn:          departsOn,
			ReturnsOn:          returnsOn,
			RequestDeadline:    departsOn.AddDate(0, 0, -3),
			Capacity:           20,
		})
		if createErr != nil {
			return createErr
		}
		tripID = created.ID
		log.Printf("created demo trip %q (%s)", demoTripTitle, created.Slug)
	default:
		return err
	}

	items, err := trips.Items(ctx, tripID)
	if err != nil {
		return err
	}
	existing := make(map[string]bool, len(items))
	for _, item := range items {
		existing[item.Name] = true
	}
	nextPosition := len(items)
	for _, item := range []struct {
		name        string
		description string
		price       int64
	}{
		{"Tokyo assorted snack box", "A mix of popular Japanese snacks and limited flavors.", 185000},
		{"Japanese stationery set", "Pens, notebooks, and small stationery finds from Tokyo.", 120000},
		{"Japanese sunscreen", "A popular lightweight sunscreen from a Japanese drugstore.", 165000},
	} {
		if existing[item.name] {
			continue
		}
		name, description, price, position := item.name, item.description, item.price, nextPosition
		if _, err := trips.AddItem(ctx, jastiperID, tripID, trip.ItemParams{
			Name:           &name,
			Description:    &description,
			ReferencePrice: &price,
			Position:       &position,
		}); err != nil {
			return err
		}
		nextPosition++
		log.Printf("created demo catalog item %q", item.name)
	}
	return nil
}

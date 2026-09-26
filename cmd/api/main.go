package main

import (
	"log"

	"github.com/Titip-Dong/titipdong-be/internal/platform/httpserver"
)

func main() {
	app := httpserver.New()

	log.Fatal(app.Listen(":8080"))
}

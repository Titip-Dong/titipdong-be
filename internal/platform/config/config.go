package config

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	DatabaseURL string
	JWTSecret   string
	Port        string
	CORSOrigins []string
	// Browsers other than Chrome drop Secure cookies on http://localhost.
	CookieSecure   bool
	PaymentGateway string
	// Uploads are disabled when BlobConnectionString is empty.
	BlobConnectionString string
	BlobContainer        string
	BlobPublicURL        string
}

func Load() (Config, error) {
	loadLocalEnv(".env")
	c := Config{DatabaseURL: os.Getenv("DATABASE_URL"), JWTSecret: os.Getenv("JWT_SECRET"), Port: os.Getenv("PORT")}
	if c.Port == "" {
		c.Port = "8080"
	}
	c.CookieSecure = os.Getenv("COOKIE_SECURE") != "false"
	c.PaymentGateway = os.Getenv("PAYMENT_GATEWAY")
	if c.PaymentGateway == "" {
		c.PaymentGateway = "fake"
	}
	if c.PaymentGateway != "fake" {
		return Config{}, fmt.Errorf("PAYMENT_GATEWAY %q is not supported yet; use fake", c.PaymentGateway)
	}
	c.BlobConnectionString = os.Getenv("AZURE_STORAGE_CONNECTION_STRING")
	c.BlobPublicURL = os.Getenv("BLOB_PUBLIC_URL")
	c.BlobContainer = os.Getenv("BLOB_CONTAINER")
	if c.BlobContainer == "" {
		c.BlobContainer = "uploads"
	}
	origins := os.Getenv("CORS_ORIGINS")
	if origins == "" {
		origins = "http://localhost:5173"
	}
	for _, o := range strings.Split(origins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			c.CORSOrigins = append(c.CORSOrigins, o)
		}
	}
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if len(c.JWTSecret) < 32 {
		return Config{}, fmt.Errorf("JWT_SECRET must be at least 32 characters")
	}
	return c, nil
}

func loadLocalEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = value[1 : len(value)-1]
		}
		if ok && os.Getenv(key) == "" {
			_ = os.Setenv(key, value)
		}
	}
}

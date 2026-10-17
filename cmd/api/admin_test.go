package main

import (
	"testing"

	"github.com/Titip-Dong/titipdong-be/internal/auth"
)

func TestParseCreateAdmin(t *testing.T) {
	p, err := parseCreateAdmin([]string{"-name", "Admin", "-email", "admin@example.com"}, "rahasia123")
	if err != nil || p.Name != "Admin" || p.Email != "admin@example.com" || p.Role != auth.RoleAdmin || p.Password != "rahasia123" {
		t.Fatalf("parseCreateAdmin() = %+v, %v", p, err)
	}
	for name, tc := range map[string]struct {
		args     []string
		password string
	}{
		"no email":       {[]string{"-name", "Admin"}, "rahasia123"},
		"no password":    {[]string{"-name", "Admin", "-email", "a@example.com"}, ""},
		"short password": {[]string{"-name", "Admin", "-email", "a@example.com"}, "short"},
		"unknown flag":   {[]string{"-role", "admin"}, "rahasia123"},
	} {
		if _, err := parseCreateAdmin(tc.args, tc.password); err == nil {
			t.Errorf("%s: parseCreateAdmin() succeeded", name)
		}
	}
}

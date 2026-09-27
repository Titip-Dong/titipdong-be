package auth

import "testing"

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := hashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("hashPassword() error = %v", err)
	}
	if !verifyPassword(hash, "correct-horse-battery-staple") {
		t.Fatal("verifyPassword() rejected the original password")
	}
	if verifyPassword(hash, "wrong-password") {
		t.Fatal("verifyPassword() accepted an incorrect password")
	}
}

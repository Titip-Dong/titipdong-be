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

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	for _, encoded := range []string{"", "plain", "$argon2i$v=19$m=65536,t=3,p=4$c2FsdA$a2V5", "$argon2id$v=19$bad$c2FsdA$a2V5"} {
		if verifyPassword(encoded, "anything") {
			t.Errorf("verifyPassword(%q) = true, want false", encoded)
		}
	}
}

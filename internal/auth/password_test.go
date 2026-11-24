package auth

import "testing"

func TestPassword(t *testing.T) {
	hash, err := HashPassword("a-long-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "a-long-test-password") || VerifyPassword(hash, "wrong-password") {
		t.Fatal("password verification mismatch")
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
}

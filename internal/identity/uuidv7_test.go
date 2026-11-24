package identity

import (
	"regexp"
	"testing"
)

func TestNewID(t *testing.T) {
	id := NewID()
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("invalid UUIDv7: %q", id)
	}
}

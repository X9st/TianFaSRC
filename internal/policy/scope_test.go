package policy

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/X9st/TianFaSRC/internal/task"
)

func scope(t *testing.T, effect, kind, value string) task.ScopeEntry {
	t.Helper()
	entry, err := task.NormalizeScope(task.ScopeEntryInput{Effect: effect, Kind: kind, Value: value})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestScopeEnforcerDenyAndAllDNSAddresses(t *testing.T) {
	ctx := context.Background()
	e := Enforcer{Resolver: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("203.0.113.10"), netip.MustParseAddr("203.0.113.11")}, nil
	}}
	entries := []task.ScopeEntry{scope(t, "allow", "domain", "*.example.test"), scope(t, "deny", "ip", "203.0.113.11")}
	if _, err := e.Authorize(ctx, "https://app.example.test/", entries, 3); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("deny did not win: %v", err)
	}
	entries = entries[:1]
	target, err := e.Authorize(ctx, "https://app.example.test/admin/", entries, 3)
	if err != nil || target.ScopeVersion != 3 || len(target.Addresses) != 2 {
		t.Fatalf("authorized target=%+v err=%v", target, err)
	}
	if _, err := e.Authorize(ctx, "https://example.test/", entries, 3); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("wildcard incorrectly matched apex: %v", err)
	}
}

func TestScopeEnforcerProtectedAndAmbiguousTargets(t *testing.T) {
	ctx := context.Background()
	e := Enforcer{Resolver: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}}
	entries := []task.ScopeEntry{scope(t, "allow", "domain", "example.test"), scope(t, "allow", "cidr", "127.0.0.0/8")}
	for _, raw := range []string{"https://example.test/", "http://127.0.0.1/", "http://[::ffff:127.0.0.1]/"} {
		if _, err := e.Authorize(ctx, raw, entries, 1); !errors.Is(err, ErrProtectedTarget) {
			t.Errorf("protected target %q: %v", raw, err)
		}
	}
	e.TestOnlyAllowPrivate = true
	e.Resolver = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("203.0.113.2")}, nil
	}
	for _, raw := range []string{"https://example.test/%2e%2e/private", "https://example.test/a%2fb", "https://example.test/a/../b", "file:///etc/passwd", "https://user:pass@example.test/"} {
		if _, err := e.Authorize(ctx, raw, entries, 1); !errors.Is(err, ErrOutOfScope) {
			t.Errorf("ambiguous target %q: %v", raw, err)
		}
	}
}

func TestURLPrefixAndCIDR(t *testing.T) {
	ctx := context.Background()
	e := Enforcer{Resolver: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("203.0.113.42")}, nil
	}}
	entries := []task.ScopeEntry{scope(t, "allow", "url_prefix", "https://app.example.test/admin/"), scope(t, "deny", "cidr", "198.51.100.0/24")}
	if _, err := e.Authorize(ctx, "https://app.example.test/admin/users", entries, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Authorize(ctx, "https://app.example.test/public", entries, 1); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("escaped URL prefix: %v", err)
	}
	entries = []task.ScopeEntry{scope(t, "allow", "cidr", "203.0.113.0/24")}
	if _, err := e.Authorize(ctx, "https://app.example.test/", entries, 1); err != nil {
		t.Fatal(err)
	}
}

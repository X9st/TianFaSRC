package task

import "testing"

func TestNormalizeScope(t *testing.T) {
	entry, err := NormalizeScope(ScopeEntryInput{Effect: "allow", Kind: "domain", Value: "*.EXAMPLE.COM.", Ports: []int{443, 80, 443}})
	if err != nil {
		t.Fatal(err)
	}
	if entry.NormalizedValue != "*.example.com" || len(entry.Ports) != 2 {
		t.Fatalf("unexpected entry: %#v", entry)
	}
	if _, err := NormalizeScope(ScopeEntryInput{Effect: "allow", Kind: "url_prefix", Value: "https://user:pass@example.com/"}); err == nil {
		t.Fatal("userinfo accepted")
	}
	if _, err := NormalizeScope(ScopeEntryInput{Effect: "allow", Kind: "url_prefix", Value: "https://example.com\\@evil.test/"}); err == nil {
		t.Fatal("backslash accepted")
	}
	for _, value := range []string{"https://example.com/a%2fb/", "https://example.com/%252e%252e/", "https://example.com/a%5cb/"} {
		if _, err := NormalizeScope(ScopeEntryInput{Effect: "allow", Kind: "url_prefix", Value: value}); err == nil {
			t.Fatalf("ambiguous URL accepted: %s", value)
		}
	}
	if _, err := NormalizeScope(ScopeEntryInput{Effect: "allow", Kind: "domain", Value: "127.0.0.1"}); err == nil {
		t.Fatal("IP accepted as domain")
	}
	if _, err := NormalizeScope(ScopeEntryInput{Effect: "allow", Kind: "domain", Value: "bad_name.example"}); err == nil {
		t.Fatal("underscore accepted")
	}
}

package tool

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/X9st/TianFaSRC/internal/policy"
	"github.com/X9st/TianFaSRC/internal/task"
)

func TestHTTPReadRedirectIsReauthorized(t *testing.T) {
	var escaped atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "http://127.0.0.2:"+strconv.Itoa(portOf(t, r.Host))+"/escape", http.StatusFound)
			return
		}
		escaped.Add(1)
		_, _ = w.Write([]byte("escaped"))
	}))
	defer server.Close()
	port := portOf(t, strings.TrimPrefix(server.URL, "http://"))
	entry, err := task.NormalizeScope(task.ScopeEntryInput{Effect: "allow", Kind: "ip", Value: "127.0.0.1", Ports: []int{port}})
	if err != nil {
		t.Fatal(err)
	}
	h := HTTPRead{Enforcer: policy.Enforcer{TestOnlyAllowPrivate: true}, Scopes: func(context.Context) ([]task.ScopeEntry, int64, error) { return []task.ScopeEntry{entry}, 1, nil }}
	input, _ := json.Marshal(HTTPInput{URL: server.URL + "/start"})
	_, _, err = h.Execute(context.Background(), input)
	if !errors.Is(err, policy.ErrOutOfScope) || escaped.Load() != 0 {
		t.Fatalf("redirect escaped scope: err=%v count=%d", err, escaped.Load())
	}
}

func TestHTTPReadPinsDNSAndChecksScopeVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("observed")) }))
	defer server.Close()
	port := portOf(t, strings.TrimPrefix(server.URL, "http://"))
	entry, err := task.NormalizeScope(task.ScopeEntryInput{Effect: "allow", Kind: "domain", Value: "authorized.example.test", Ports: []int{port}})
	if err != nil {
		t.Fatal(err)
	}
	var lookups atomic.Int32
	e := policy.Enforcer{TestOnlyAllowPrivate: true, Resolver: func(context.Context, string) ([]netip.Addr, error) {
		lookups.Add(1)
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}}
	url := "http://authorized.example.test:" + strconv.Itoa(port) + "/"
	h := HTTPRead{Enforcer: e, Scopes: func(context.Context) ([]task.ScopeEntry, int64, error) { return []task.ScopeEntry{entry}, 1, nil }}
	input, _ := json.Marshal(HTTPInput{URL: url})
	result, version, err := h.Execute(context.Background(), input)
	if err != nil || version != 1 || string(result.Output) != "observed" || result.RemoteIP != "127.0.0.1" || lookups.Load() != 1 {
		t.Fatalf("result=%+v version=%d lookups=%d err=%v", result, version, lookups.Load(), err)
	}
	var reads atomic.Int32
	h.Scopes = func(context.Context) ([]task.ScopeEntry, int64, error) {
		if reads.Add(1) == 1 {
			return []task.ScopeEntry{entry}, 1, nil
		}
		return []task.ScopeEntry{entry}, 2, nil
	}
	if _, _, err := h.Execute(context.Background(), input); !errors.Is(err, policy.ErrOutOfScope) {
		t.Fatalf("scope change not enforced: %v", err)
	}
}

func portOf(t *testing.T, hostport string) int {
	t.Helper()
	_, text, err := net.SplitHostPort(hostport)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(text)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

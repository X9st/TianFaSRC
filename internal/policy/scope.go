package policy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/X9st/TianFaSRC/internal/task"
	"golang.org/x/net/idna"
)

var ErrOutOfScope = errors.New("目标不在授权范围内")
var ErrProtectedTarget = errors.New("目标属于平台保护地址")

type Resolver func(context.Context, string) ([]netip.Addr, error)

type Enforcer struct {
	Resolver Resolver
	// TestOnlyAllowPrivate permits local integration servers; production never sets it.
	TestOnlyAllowPrivate bool
	ProtectedHosts       []string
	ProtectedIPs         []netip.Addr
}

type Target struct {
	URL          *url.URL
	Host         string
	Port         int
	Addresses    []netip.Addr
	ScopeVersion int64
}

// Denial preserves the decision data needed to audit a rejected target.
// The exact input remains in the restricted ToolCall record; audit consumers
// receive a URL without credentials or query values.
type Denial struct {
	Cause       error
	Rule        string
	Target      string
	ResolvedIPs []string
}

func (d *Denial) Error() string { return d.Cause.Error() }
func (d *Denial) Unwrap() error { return d.Cause }

func redactedTarget(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func (e Enforcer) Authorize(ctx context.Context, raw string, entries []task.ScopeEntry, version int64) (Target, error) {
	var addresses []netip.Addr
	deny := func(cause error, rule string) (Target, error) {
		ips := make([]string, 0, len(addresses))
		for _, addr := range addresses {
			ips = append(ips, addr.Unmap().String())
		}
		return Target{}, &Denial{Cause: cause, Rule: rule, Target: redactedTarget(raw), ResolvedIPs: ips}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return deny(fmt.Errorf("%w: 无效 URL", ErrOutOfScope), "invalid_url")
	}
	if strings.ContainsAny(raw, "\\\x00") || strings.ContainsAny(u.EscapedPath(), "\\") {
		return deny(fmt.Errorf("%w: 路径不明确", ErrOutOfScope), "ambiguous_path")
	}
	lowerPath := strings.ToLower(u.EscapedPath())
	for _, fragment := range []string{"%2f", "%5c", "%25", "%2e", "%00"} {
		if strings.Contains(lowerPath, fragment) {
			return deny(fmt.Errorf("%w: 路径编码不明确", ErrOutOfScope), "ambiguous_encoding")
		}
	}
	for _, segment := range strings.Split(u.EscapedPath(), "/") {
		if segment == "." || segment == ".." {
			return deny(fmt.Errorf("%w: 路径未规范化", ErrOutOfScope), "unnormalized_path")
		}
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if _, parseErr := netip.ParseAddr(host); parseErr != nil {
		host, err = idna.Lookup.ToASCII(host)
	}
	if err != nil || host == "" {
		return deny(fmt.Errorf("%w: 无效主机名", ErrOutOfScope), "invalid_host")
	}
	port := 80
	if u.Scheme == "https" {
		port = 443
	}
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return deny(fmt.Errorf("%w: 无效端口", ErrOutOfScope), "invalid_port")
		}
	}
	if protectedHost(host, e.ProtectedHosts) {
		return deny(ErrProtectedTarget, "protected_host")
	}
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
		addresses = []netip.Addr{ip.Unmap()}
		host = ip.Unmap().String()
	} else {
		resolver := e.Resolver
		if resolver == nil {
			resolver = resolveSystem
		}
		addresses, err = resolver(ctx, host)
		if err != nil || len(addresses) == 0 {
			return deny(fmt.Errorf("%w: DNS 解析失败", ErrOutOfScope), "dns_failed")
		}
	}
	allowHost := false
	for _, entry := range entries {
		if entry.Effect == "allow" && matches(entry, host, port, u.EscapedPath(), nil, u.Scheme) {
			allowHost = true
		}
	}
	for _, addr := range addresses {
		addr = addr.Unmap()
		if !addr.IsValid() || addr.IsLinkLocalUnicast() || addr.IsMulticast() || addr.IsUnspecified() || (!e.TestOnlyAllowPrivate && (addr.IsPrivate() || addr.IsLoopback())) || slices.Contains(e.ProtectedIPs, addr) {
			return deny(ErrProtectedTarget, "protected_ip")
		}
		allowedIP := false
		for _, entry := range entries {
			if matches(entry, host, port, u.EscapedPath(), &addr, u.Scheme) {
				if entry.Effect == "deny" {
					return deny(ErrOutOfScope, "explicit_deny:"+entry.Kind+":"+entry.NormalizedValue)
				}
				allowedIP = true
			}
		}
		if !allowHost && !allowedIP {
			return deny(ErrOutOfScope, "allowlist_miss")
		}
	}
	for _, entry := range entries {
		if entry.Effect == "deny" && matches(entry, host, port, u.EscapedPath(), nil, u.Scheme) {
			return deny(ErrOutOfScope, "explicit_deny:"+entry.Kind+":"+entry.NormalizedValue)
		}
	}
	u.Host = net.JoinHostPort(host, strconv.Itoa(port))
	return Target{URL: u, Host: host, Port: port, Addresses: addresses, ScopeVersion: version}, nil
}

func resolveSystem(ctx context.Context, host string) ([]netip.Addr, error) {
	items, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	return items, nil
}

func protectedHost(host string, extras []string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "metadata.google.internal" || host == "169.254.169.254" {
		return true
	}
	for _, item := range extras {
		if strings.EqualFold(host, item) {
			return true
		}
	}
	return false
}

func matches(entry task.ScopeEntry, host string, port int, requestPath string, addr *netip.Addr, scheme string) bool {
	if len(entry.Ports) > 0 && !slices.Contains(entry.Ports, port) {
		return false
	}
	switch entry.Kind {
	case "domain":
		value := entry.NormalizedValue
		if strings.HasPrefix(value, "*.") {
			base := strings.TrimPrefix(value, "*.")
			return strings.HasSuffix(host, "."+base) && host != base
		}
		return host == value
	case "service":
		return net.JoinHostPort(host, strconv.Itoa(port)) == entry.NormalizedValue
	case "url_prefix":
		prefix, err := url.Parse(entry.NormalizedValue)
		if err != nil {
			return false
		}
		if prefix.Scheme != scheme || prefix.Hostname() != host || prefix.Port() != strconv.Itoa(port) {
			return false
		}
		if requestPath == "" {
			requestPath = "/"
		}
		return strings.HasPrefix(requestPath, prefix.EscapedPath())
	case "ip":
		if addr == nil {
			return false
		}
		return addr.String() == entry.NormalizedValue
	case "cidr":
		if addr == nil {
			return false
		}
		prefix, err := netip.ParsePrefix(entry.NormalizedValue)
		return err == nil && prefix.Contains(*addr)
	}
	return false
}

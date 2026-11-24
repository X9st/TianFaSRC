package task

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

type ScopeEntryInput struct {
	Effect     string `json:"effect"`
	Kind       string `json:"kind"`
	Value      string `json:"value"`
	Ports      []int  `json:"ports"`
	PathPrefix string `json:"path_prefix,omitempty"`
}

type ScopeEntry struct {
	ScopeEntryInput
	ID              string `json:"id"`
	NormalizedValue string `json:"normalized_value"`
}

func NormalizeScope(entry ScopeEntryInput) (ScopeEntry, error) {
	if entry.Effect != "allow" && entry.Effect != "deny" {
		return ScopeEntry{}, fmt.Errorf("scope effect must be allow or deny")
	}
	ports := append([]int{}, entry.Ports...)
	for _, port := range ports {
		if port < 1 || port > 65535 {
			return ScopeEntry{}, fmt.Errorf("invalid port %d", port)
		}
	}
	slices.Sort(ports)
	ports = slices.Compact(ports)
	entry.Ports = ports
	entry.Value = strings.TrimSpace(entry.Value)
	var normalized string
	switch entry.Kind {
	case "domain":
		wildcard := strings.HasPrefix(entry.Value, "*.")
		host := strings.TrimPrefix(entry.Value, "*.")
		if _, err := netip.ParseAddr(host); err == nil {
			return ScopeEntry{}, fmt.Errorf("IP address must use ip scope")
		}
		var err error
		normalized, err = normalizeHost(host)
		if err != nil {
			return ScopeEntry{}, err
		}
		if wildcard {
			normalized = "*." + normalized
		}
	case "ip":
		address, err := netip.ParseAddr(entry.Value)
		if err != nil {
			return ScopeEntry{}, fmt.Errorf("invalid IP: %w", err)
		}
		normalized = address.Unmap().String()
	case "cidr":
		prefix, err := netip.ParsePrefix(entry.Value)
		if err != nil {
			return ScopeEntry{}, fmt.Errorf("invalid CIDR: %w", err)
		}
		normalized = prefix.Masked().String()
	case "url_prefix":
		if strings.ContainsAny(entry.Value, "\\#") {
			return ScopeEntry{}, fmt.Errorf("URL prefix contains forbidden characters")
		}
		u, err := url.Parse(entry.Value)
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return ScopeEntry{}, fmt.Errorf("invalid URL prefix")
		}
		host, err := normalizeHost(u.Hostname())
		if err != nil {
			return ScopeEntry{}, err
		}
		port := u.Port()
		if port == "" {
			if u.Scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return ScopeEntry{}, fmt.Errorf("invalid URL port")
		}
		if len(ports) > 0 && !slices.Contains(ports, portNumber) {
			return ScopeEntry{}, fmt.Errorf("URL port conflicts with ports")
		}
		entry.Ports = []int{portNumber}
		entry.PathPrefix = u.EscapedPath()
		if entry.PathPrefix == "" {
			entry.PathPrefix = "/"
		}
		lowerPath := strings.ToLower(entry.PathPrefix)
		for _, encoded := range []string{"%2f", "%5c", "%25", "%2e", "%00"} {
			if strings.Contains(lowerPath, encoded) {
				return ScopeEntry{}, fmt.Errorf("URL prefix contains ambiguous encoding")
			}
		}
		normalized = u.Scheme + "://" + net.JoinHostPort(host, port) + entry.PathPrefix
	case "service":
		host, port, err := net.SplitHostPort(entry.Value)
		if err != nil {
			return ScopeEntry{}, fmt.Errorf("service must be host:port")
		}
		host, err = normalizeHost(host)
		if err != nil {
			return ScopeEntry{}, err
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return ScopeEntry{}, fmt.Errorf("invalid service port")
		}
		entry.Ports = []int{portNumber}
		normalized = net.JoinHostPort(host, port)
	default:
		return ScopeEntry{}, fmt.Errorf("invalid scope kind")
	}
	if entry.Kind != "url_prefix" && entry.PathPrefix != "" {
		return ScopeEntry{}, fmt.Errorf("path prefix only applies to URL scope")
	}
	return ScopeEntry{ScopeEntryInput: entry, NormalizedValue: normalized}, nil
}

func normalizeHost(host string) (string, error) {
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	if address, err := netip.ParseAddr(host); err == nil {
		return address.Unmap().String(), nil
	}
	host, err := idna.Lookup.ToASCII(strings.ToLower(host))
	if err != nil || len(host) < 1 || len(host) > 253 || strings.ContainsAny(host, ":/@*% ") {
		return "", fmt.Errorf("invalid hostname")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", fmt.Errorf("invalid hostname label")
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-') {
				return "", fmt.Errorf("invalid hostname label")
			}
		}
	}
	return host, nil
}

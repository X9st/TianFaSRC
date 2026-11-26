package recordproxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"

	"github.com/X9st/TianFaSRC/internal/policy"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/X9st/TianFaSRC/internal/traffic"
)

type Proxy struct {
	CA                 *CA
	Tokens             TokenStore
	Tasks              task.Store
	Traffic            traffic.Store
	Enforcer           policy.Enforcer
	TestOnlyTLSRootCAs *x509.CertPool
}

func proxyPassword(r *http.Request) string {
	value := r.Header.Get("Proxy-Authorization")
	if !strings.HasPrefix(value, "Basic ") {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, "Basic "))
	if err != nil {
		return ""
	}
	name, password, ok := strings.Cut(string(decoded), ":")
	if !ok || name != "tianfa" {
		return ""
	}
	return password
}

func (p Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.CA == nil || p.Tokens.Pool == nil {
		http.Error(w, "proxy unavailable", http.StatusServiceUnavailable)
		return
	}
	session, err := p.Tokens.Authenticate(r.Context(), proxyPassword(r))
	if err != nil {
		w.Header().Set("Proxy-Authenticate", `Basic realm="TianFa task proxy"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect {
		p.connect(w, r, session)
		return
	}
	if !r.URL.IsAbs() || r.URL.Scheme != "http" {
		http.Error(w, "absolute HTTP URL required", http.StatusBadRequest)
		return
	}
	result, err := p.forward(r.Context(), session, r, r.URL.String())
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	for key, values := range result.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.Header().Set("Via", "1.1 TianFa")
	w.WriteHeader(result.StatusCode)
	_, _ = w.Write(result.Body)
}

func (p Proxy) connect(w http.ResponseWriter, r *http.Request, session Session) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || host == "" || port == "" {
		http.Error(w, "invalid CONNECT target", 400)
		return
	}
	detail, err := p.Tasks.Get(r.Context(), session.OrganizationID, session.TaskID)
	if err != nil {
		http.Error(w, "task unavailable", 403)
		return
	}
	if _, err := p.Enforcer.Authorize(r.Context(), "https://"+net.JoinHostPort(host, port)+"/", detail.Scope, detail.Task.ScopeVersion); err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	certificate, err := p.CA.HostCertificate(host)
	if err != nil {
		http.Error(w, "certificate failure", 503)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "CONNECT unsupported", 503)
		return
	}
	connection, buffer, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer connection.Close()
	_, _ = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	if err := buffer.Flush(); err != nil {
		return
	}
	tlsConn := tls.Server(connection, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	if err := tlsConn.HandshakeContext(r.Context()); err != nil {
		return
	}
	defer tlsConn.Close()
	reader := bufio.NewReader(tlsConn)
	writer := bufio.NewWriter(tlsConn)
	for {
		_ = tlsConn.SetReadDeadline(time.Now().Add(60 * time.Second))
		request, err := http.ReadRequest(reader)
		if err != nil {
			return
		}
		if request.URL.IsAbs() || !sameHost(request.Host, r.Host) {
			_ = writeTunnelError(writer, 403, "tunnel host mismatch")
			return
		}
		urlText := "https://" + r.Host + request.URL.RequestURI()
		requestCtx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		result, callErr := p.forward(requestCtx, session, request, urlText)
		cancel()
		_ = request.Body.Close()
		if callErr != nil {
			_ = writeTunnelError(writer, 403, callErr.Error())
			return
		}
		response := &http.Response{StatusCode: result.StatusCode, Status: fmt.Sprintf("%d %s", result.StatusCode, http.StatusText(result.StatusCode)), Header: result.Header, Body: io.NopCloser(bytes.NewReader(result.Body)), ContentLength: int64(len(result.Body)), ProtoMajor: 1, ProtoMinor: 1}
		response.Header.Set("Via", "1.1 TianFa")
		if err := response.Write(writer); err != nil {
			return
		}
		if err := writer.Flush(); err != nil {
			return
		}
		if request.Close {
			return
		}
	}
}

func sameHost(left, right string) bool {
	a, ap, ae := net.SplitHostPort(left)
	b, bp, be := net.SplitHostPort(right)
	if ae != nil {
		a = left
		ap = "443"
	}
	if be != nil {
		b = right
		bp = "443"
	}
	return strings.EqualFold(strings.TrimSuffix(a, "."), strings.TrimSuffix(b, ".")) && ap == bp
}
func writeTunnelError(writer *bufio.Writer, status int, message string) error {
	response := &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Header: http.Header{"Content-Type": {"text/plain; charset=utf-8"}}, Body: io.NopCloser(strings.NewReader(message)), ContentLength: int64(len(message)), ProtoMajor: 1, ProtoMinor: 1, Close: true}
	if err := response.Write(writer); err != nil {
		return err
	}
	return writer.Flush()
}

type forwarded struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func (p Proxy) forward(ctx context.Context, session Session, request *http.Request, rawURL string) (forwarded, error) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		return forwarded{}, errors.New("记录代理当前仅允许 GET/HEAD")
	}
	detail, err := p.Tasks.Get(ctx, session.OrganizationID, session.TaskID)
	if err != nil {
		return forwarded{}, err
	}
	target, err := p.Enforcer.Authorize(ctx, rawURL, detail.Scope, detail.Task.ScopeVersion)
	if err != nil {
		return forwarded{}, err
	}
	if request.Body != nil {
		data, err := io.ReadAll(io.LimitReader(request.Body, 1))
		if err != nil || len(data) > 0 {
			return forwarded{}, errors.New("记录代理只允许无正文请求")
		}
	}
	outbound, err := http.NewRequestWithContext(ctx, request.Method, target.URL.String(), nil)
	if err != nil {
		return forwarded{}, err
	}
	outbound.Header = request.Header.Clone()
	outbound.Header.Del("Proxy-Authorization")
	outbound.Header.Del("Connection")
	outbound.Header.Del("Proxy-Connection")
	outbound.Header.Del("Keep-Alive")
	outbound.Header.Del("Transfer-Encoding")
	requestRaw, err := httputil.DumpRequestOut(outbound, true)
	if err != nil {
		return forwarded{}, err
	}
	address := target.Addresses[0]
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSClientConfig: &tls.Config{RootCAs: p.TestOnlyTLSRootCAs, MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp", net.JoinHostPort(address.String(), strconv.Itoa(target.Port)))
	}}
	defer transport.CloseIdleConnections()
	requestedAt := time.Now()
	response, err := transport.RoundTrip(outbound)
	if err != nil {
		return forwarded{}, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
	if err != nil {
		return forwarded{}, err
	}
	if len(data) > 16<<20 {
		return forwarded{}, errors.New("响应超过 16 MiB 上限")
	}
	respondedAt := time.Now()
	response.Body = io.NopCloser(bytes.NewReader(data))
	responseRaw, err := httputil.DumpResponse(response, true)
	if err != nil {
		return forwarded{}, err
	}
	latest, err := p.Tasks.Get(ctx, session.OrganizationID, session.TaskID)
	if err != nil || latest.Task.ScopeVersion != detail.Task.ScopeVersion {
		return forwarded{}, policy.ErrOutOfScope
	}
	tlsInfo := map[string]any{}
	if response.TLS != nil {
		tlsInfo = map[string]any{"version": response.TLS.Version, "cipher_suite": response.TLS.CipherSuite, "server_name": response.TLS.ServerName, "negotiated_protocol": response.TLS.NegotiatedProtocol}
	}
	_, err = p.Traffic.Record(ctx, traffic.RecordInput{OrganizationID: session.OrganizationID, TaskID: session.TaskID, Method: outbound.Method, URL: target.URL.String(), RemoteIP: address.String(), StatusCode: response.StatusCode, TLS: tlsInfo, RequestHeaders: outbound.Header, ResponseHeaders: response.Header, RequestRaw: requestRaw, ResponseRaw: responseRaw, RequestedAt: requestedAt, RespondedAt: respondedAt})
	if err != nil {
		return forwarded{}, err
	}
	return forwarded{StatusCode: response.StatusCode, Header: response.Header.Clone(), Body: data}, nil
}

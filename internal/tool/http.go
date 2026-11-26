package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/X9st/TianFaSRC/internal/policy"
	"github.com/X9st/TianFaSRC/internal/task"
)

type ScopeReader func(context.Context) ([]task.ScopeEntry, int64, error)

type HTTPRead struct {
	Enforcer policy.Enforcer
	Scopes   ScopeReader
}

type HTTPInput struct {
	URL    string `json:"url"`
	Method string `json:"method"`
}

type Result struct {
	Summary     string
	Output      []byte
	ExitCode    int
	ContentType string
	RemoteIP    string
	Truncated   bool
}

func decodeStrict(raw json.RawMessage, dst any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("输入必须是单个 JSON 对象")
	}
	return nil
}

func (h HTTPRead) Execute(ctx context.Context, raw json.RawMessage) (Result, int64, error) {
	var input HTTPInput
	if err := decodeStrict(raw, &input); err != nil {
		return Result{}, 0, err
	}
	if input.Method == "" {
		input.Method = http.MethodGet
	}
	if input.Method != http.MethodGet && input.Method != http.MethodHead {
		return Result{}, 0, errors.New("http.read 只支持 GET 和 HEAD")
	}
	if h.Scopes == nil {
		return Result{}, 0, errors.New("缺少授权范围读取器")
	}
	urlText := input.URL
	var originalVersion int64
	for redirects := 0; redirects <= 5; redirects++ {
		entries, version, err := h.Scopes(ctx)
		if err != nil {
			return Result{}, originalVersion, err
		}
		if redirects > 0 && version != originalVersion {
			return Result{}, originalVersion, fmt.Errorf("%w: 授权范围已更新", policy.ErrOutOfScope)
		}
		if redirects == 0 {
			originalVersion = version
		}
		target, err := h.Enforcer.Authorize(ctx, urlText, entries, version)
		if err != nil {
			return Result{}, originalVersion, err
		}
		address := target.Addresses[0]
		dialer := &net.Dialer{Timeout: 8 * time.Second}
		transport := &http.Transport{
			DisableKeepAlives: true,
			Proxy:             nil,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "tcp", net.JoinHostPort(address.String(), strconv.Itoa(target.Port)))
			},
		}
		client := &http.Client{Transport: transport, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		request, err := http.NewRequestWithContext(ctx, input.Method, target.URL.String(), nil)
		if err != nil {
			transport.CloseIdleConnections()
			return Result{}, originalVersion, err
		}
		request.Header.Set("User-Agent", "TianFa/1.0 authorized-validation")
		response, err := client.Do(request)
		if err != nil {
			transport.CloseIdleConnections()
			return Result{}, originalVersion, err
		}
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			location := response.Header.Get("Location")
			response.Body.Close()
			transport.CloseIdleConnections()
			if location == "" {
				return Result{}, originalVersion, errors.New("重定向缺少 Location")
			}
			next, err := target.URL.Parse(location)
			if err != nil {
				return Result{}, originalVersion, err
			}
			urlText = next.String()
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
		response.Body.Close()
		transport.CloseIdleConnections()
		if readErr != nil {
			return Result{}, originalVersion, readErr
		}
		truncated := len(body) > 4<<20
		if truncated {
			body = body[:4<<20]
		}
		// Scope may be narrowed while a response is in flight. Never return stale data.
		_, latest, err := h.Scopes(ctx)
		if err != nil || latest != originalVersion {
			return Result{}, originalVersion, fmt.Errorf("%w: 授权范围已更新", policy.ErrOutOfScope)
		}
		return Result{Summary: fmt.Sprintf("%s %s → HTTP %d (%d 字节)", input.Method, target.URL.Redacted(), response.StatusCode, len(body)), Output: body, ExitCode: response.StatusCode, ContentType: response.Header.Get("Content-Type"), RemoteIP: address.String(), Truncated: truncated}, originalVersion, nil
	}
	return Result{}, originalVersion, errors.New("重定向次数超过上限")
}

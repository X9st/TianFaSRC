package mcpclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/policy"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,79}$`)
var imagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@:-]{0,239}$`)

type Server struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Transport string   `json:"transport"`
	Endpoint  string   `json:"endpoint,omitempty"`
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args,omitempty"`
	Enabled   bool     `json:"enabled"`
}

type DiscoveredTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	SchemaHash  string          `json:"schema_hash"`
	Enabled     bool            `json:"enabled"`
}

type Store struct {
	Pool                 *pgxpool.Pool
	Enforcer             policy.Enforcer
	TestOnlyAllowHTTPURL bool
}

func (s Store) Create(ctx context.Context, orgID, actorID string, input Server) (Server, error) {
	if !namePattern.MatchString(input.Name) || (input.Transport != "stdio" && input.Transport != "streamable_http") {
		return Server{}, task.ErrInvalid
	}
	if input.Transport == "streamable_http" {
		u, err := url.Parse(input.Endpoint)
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(s.TestOnlyAllowHTTPURL && u.Scheme == "http")) || u.Host == "" || input.Command != "" {
			return Server{}, task.ErrInvalid
		}
		entry, err := task.NormalizeScope(task.ScopeEntryInput{Effect: "allow", Kind: "url_prefix", Value: input.Endpoint})
		if err != nil {
			return Server{}, task.ErrInvalid
		}
		if _, err := s.Enforcer.Authorize(ctx, input.Endpoint, []task.ScopeEntry{entry}, 1); err != nil {
			return Server{}, err
		}
	} else if !imagePattern.MatchString(input.Command) || len(input.Args) > 32 || input.Endpoint != "" {
		return Server{}, task.ErrInvalid
	}
	input.ID = identity.NewID()
	input.Enabled = false
	args, _ := json.Marshal(input.Args)
	err := s.Pool.QueryRow(ctx, `INSERT INTO mcp_servers(id,organization_id,name,transport,endpoint,command,args,enabled,created_by) VALUES($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7,false,$8) RETURNING id`, input.ID, orgID, input.Name, input.Transport, input.Endpoint, input.Command, args, actorID).Scan(&input.ID)
	if err != nil {
		return Server{}, err
	}
	return input, nil
}

func (s Store) List(ctx context.Context, orgID string) ([]Server, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,name,transport,coalesce(endpoint,''),coalesce(command,''),args,enabled FROM mcp_servers WHERE organization_id=$1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Server{}
	for rows.Next() {
		var item Server
		var args []byte
		if err := rows.Scan(&item.ID, &item.Name, &item.Transport, &item.Endpoint, &item.Command, &args, &item.Enabled); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(args, &item.Args)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s Store) get(ctx context.Context, orgID, name string) (Server, error) {
	var item Server
	var args []byte
	err := s.Pool.QueryRow(ctx, `SELECT id,name,transport,coalesce(endpoint,''),coalesce(command,''),args,enabled FROM mcp_servers WHERE organization_id=$1 AND name=$2`, orgID, name).Scan(&item.ID, &item.Name, &item.Transport, &item.Endpoint, &item.Command, &args, &item.Enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return Server{}, task.ErrNotFound
	}
	if err != nil {
		return Server{}, err
	}
	_ = json.Unmarshal(args, &item.Args)
	return item, nil
}

func (s Store) session(ctx context.Context, server Server) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "tianfa", Version: "0.1.0"}, nil)
	if server.Transport == "stdio" {
		if os.Getuid() == 0 {
			return nil, errors.New("MCP stdio 不能以 root 身份启动")
		}
		args := []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "64", "--cpus", "1", "--memory", "256m", "--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()), "--tmpfs", "/tmp:rw,noexec,nosuid,size=16m", "-i", server.Command}
		args = append(args, server.Args...)
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
		return client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	}
	if server.Transport != "streamable_http" {
		return nil, task.ErrInvalid
	}
	entry, err := task.NormalizeScope(task.ScopeEntryInput{Effect: "allow", Kind: "url_prefix", Value: server.Endpoint})
	if err != nil {
		return nil, err
	}
	transport := &endpointTransport{enforcer: s.Enforcer, entry: entry}
	httpClient := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.Endpoint, HTTPClient: httpClient, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
}

type endpointTransport struct {
	enforcer policy.Enforcer
	entry    task.ScopeEntry
}

func (t *endpointTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	target, err := t.enforcer.Authorize(request.Context(), request.URL.String(), []task.ScopeEntry{t.entry}, 1)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp", net.JoinHostPort(target.Addresses[0].String(), strconv.Itoa(target.Port)))
	}}
	defer transport.CloseIdleConnections()
	return transport.RoundTrip(request)
}

func (s Store) Discover(ctx context.Context, orgID, serverName string) ([]DiscoveredTool, error) {
	server, err := s.get(ctx, orgID, serverName)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	session, err := s.session(callCtx, server)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	items := []DiscoveredTool{}
	cursor := ""
	for page := 0; page < 10; page++ {
		listed, err := session.ListTools(callCtx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, item := range listed.Tools {
			if item == nil || item.Name == "" || len(items) >= 200 {
				return nil, task.ErrInvalid
			}
			schema, err := json.Marshal(item.InputSchema)
			if err != nil || len(schema) > 64<<10 {
				return nil, task.ErrInvalid
			}
			sum := sha256.Sum256(schema)
			items = append(items, DiscoveredTool{Name: item.Name, Description: item.Description, InputSchema: schema, SchemaHash: hex.EncodeToString(sum[:])})
		}
		cursor = listed.NextCursor
		if cursor == "" {
			break
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	for i := range items {
		item := &items[i]
		if err := tx.QueryRow(ctx, `INSERT INTO mcp_tools(server_id,name,description,input_schema,schema_hash,enabled) VALUES($1,$2,$3,$4,$5,false) ON CONFLICT(server_id,name) DO UPDATE SET description=excluded.description,input_schema=excluded.input_schema,schema_hash=excluded.schema_hash,enabled=CASE WHEN mcp_tools.schema_hash=excluded.schema_hash THEN mcp_tools.enabled ELSE false END RETURNING enabled`, server.ID, item.Name, item.Description, item.InputSchema, item.SchemaHash).Scan(&item.Enabled); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return items, nil
}

func (s Store) SetEnabled(ctx context.Context, orgID, actorID, name string, enabled bool) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE mcp_servers SET enabled=$3,updated_at=now() WHERE organization_id=$1 AND name=$2`, orgID, name, enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return task.ErrNotFound
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,detail) VALUES($1,$2,$3,'mcp.server_changed','mcp_server',$4)`, identity.NewID(), orgID, actorID, map[string]any{"name": name, "enabled": enabled})
	return err
}

func (s Store) SetToolEnabled(ctx context.Context, orgID, actorID, serverName, toolName string, enabled bool) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE mcp_tools mt SET enabled=$4 FROM mcp_servers ms WHERE mt.server_id=ms.id AND ms.organization_id=$1 AND ms.name=$2 AND mt.name=$3`, orgID, serverName, toolName, enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return task.ErrNotFound
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,detail) VALUES($1,$2,$3,'mcp.tool_changed','mcp_tool',$4)`, identity.NewID(), orgID, actorID, map[string]any{"server": serverName, "tool": toolName, "enabled": enabled})
	return err
}

func (s Store) ListTools(ctx context.Context, orgID string) (map[string][]DiscoveredTool, error) {
	rows, err := s.Pool.Query(ctx, `SELECT ms.name,mt.name,mt.description,mt.input_schema,mt.schema_hash,mt.enabled FROM mcp_tools mt JOIN mcp_servers ms ON ms.id=mt.server_id WHERE ms.organization_id=$1 ORDER BY ms.name,mt.name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := map[string][]DiscoveredTool{}
	for rows.Next() {
		var server string
		var item DiscoveredTool
		if err := rows.Scan(&server, &item.Name, &item.Description, &item.InputSchema, &item.SchemaHash, &item.Enabled); err != nil {
			return nil, err
		}
		items[server] = append(items[server], item)
	}
	return items, rows.Err()
}

func (s Store) Call(ctx context.Context, orgID, runID, serverName, toolName string, input json.RawMessage) ([]byte, error) {
	server, err := s.get(ctx, orgID, serverName)
	if err != nil || !server.Enabled {
		return nil, task.ErrInvalid
	}
	var schema json.RawMessage
	var enabled bool
	err = s.Pool.QueryRow(ctx, `SELECT mt.input_schema,mt.enabled AND EXISTS(SELECT 1 FROM run_loaded_skills rls JOIN skills sk ON sk.id=rls.skill_id WHERE rls.run_id=$4 AND sk.enabled AND $2=ANY(sk.mcps)) FROM mcp_tools mt JOIN mcp_servers ms ON ms.id=mt.server_id WHERE ms.organization_id=$1 AND ms.name=$2 AND mt.name=$3`, orgID, serverName, toolName, runID).Scan(&schema, &enabled)
	if err != nil || !enabled {
		return nil, task.ErrInvalid
	}
	// Inputs are validated by the shared Tool dispatcher before this method.
	callCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	session, err := s.session(callCtx, server)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	var arguments map[string]any
	if err := json.Unmarshal(input, &arguments); err != nil {
		return nil, err
	}
	result, err := session.CallTool(callCtx, &mcp.CallToolParams{Name: toolName, Arguments: arguments})
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > 4<<20 {
		return nil, fmt.Errorf("MCP 输出超过上限: %w", task.ErrInvalid)
	}
	if result.IsError {
		return encoded, errors.New("MCP 工具报告失败")
	}
	return encoded, nil
}

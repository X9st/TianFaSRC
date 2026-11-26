package tool

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/jackc/pgx/v5"
)

var templateField = regexp.MustCompile(`\{\{([a-zA-Z][a-zA-Z0-9_]*)\}\}`)
var customName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,79}$`)

type CustomHTTP struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	URLTemplate string          `json:"url_template"`
	Method      string          `json:"method"`
	InputSchema json.RawMessage `json:"input_schema"`
	Enabled     bool            `json:"enabled"`
}

func (d Dispatcher) CreateCustom(ctx context.Context, orgID, actorID string, input CustomHTTP) (CustomHTTP, error) {
	if !customName.MatchString(input.Name) || len(input.Description) > 1000 || len(input.URLTemplate) > 4000 || (input.Method != "GET" && input.Method != "HEAD") {
		return CustomHTTP{}, task.ErrInvalid
	}
	u, err := url.Parse(input.URLTemplate)
	if err != nil || u.User != nil || u.Fragment != "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || strings.Contains(u.Host, "{{") || strings.Contains(u.Scheme, "{{") {
		return CustomHTTP{}, task.ErrInvalid
	}
	if _, err := resolveSchema(input.InputSchema); err != nil {
		return CustomHTTP{}, task.ErrInvalid
	}
	probe := templateField.ReplaceAllString(input.URLTemplate, "x")
	entry, err := task.NormalizeScope(task.ScopeEntryInput{Effect: "allow", Kind: "url_prefix", Value: strings.Split(probe, "?")[0]})
	if err != nil {
		return CustomHTTP{}, task.ErrInvalid
	}
	if _, err := d.Enforcer.Authorize(ctx, probe, []task.ScopeEntry{entry}, 1); err != nil {
		return CustomHTTP{}, err
	}
	input.ID = identity.NewID()
	input.Enabled = false
	_, err = d.Pool.Exec(ctx, `INSERT INTO custom_http_tools(id,organization_id,name,description,url_template,method,input_schema,enabled,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,false,$8)`, input.ID, orgID, input.Name, input.Description, input.URLTemplate, input.Method, input.InputSchema, actorID)
	if err != nil {
		return CustomHTTP{}, err
	}
	return input, nil
}

func (d Dispatcher) ListCustom(ctx context.Context, orgID string) ([]CustomHTTP, error) {
	rows, err := d.Pool.Query(ctx, `SELECT id,name,description,url_template,method,input_schema,enabled FROM custom_http_tools WHERE organization_id=$1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []CustomHTTP{}
	for rows.Next() {
		var item CustomHTTP
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &item.URLTemplate, &item.Method, &item.InputSchema, &item.Enabled); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (d Dispatcher) SetCustomEnabled(ctx context.Context, orgID, actorID, name string, enabled bool) error {
	tag, err := d.Pool.Exec(ctx, `UPDATE custom_http_tools SET enabled=$3,updated_at=now() WHERE organization_id=$1 AND name=$2`, orgID, name, enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return task.ErrNotFound
	}
	_, err = d.Pool.Exec(ctx, `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,detail) VALUES($1,$2,$3,'tool.custom_changed','custom_tool',$4)`, identity.NewID(), orgID, actorID, map[string]any{"name": name, "enabled": enabled})
	return err
}

func (d Dispatcher) custom(ctx context.Context, orgID, name string) (CustomHTTP, error) {
	var item CustomHTTP
	err := d.Pool.QueryRow(ctx, `SELECT id,name,description,url_template,method,input_schema,enabled FROM custom_http_tools WHERE organization_id=$1 AND name=$2`, orgID, name).Scan(&item.ID, &item.Name, &item.Description, &item.URLTemplate, &item.Method, &item.InputSchema, &item.Enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return CustomHTTP{}, task.ErrNotFound
	}
	return item, err
}

func (d Dispatcher) executeCustom(ctx context.Context, call Call) (Result, error) {
	urlText, method, err := d.customURL(ctx, call)
	if err != nil {
		return Result{}, err
	}
	h := HTTPRead{Enforcer: d.Enforcer, Scopes: func(ctx context.Context) ([]task.ScopeEntry, int64, error) {
		current, err := (task.Store{Pool: d.Pool}).Get(ctx, call.OrganizationID, call.TaskID)
		return current.Scope, current.Task.ScopeVersion, err
	}}
	input, _ := json.Marshal(HTTPInput{URL: urlText, Method: method})
	result, _, err := h.Execute(ctx, input)
	return result, err
}

func (d Dispatcher) customURL(ctx context.Context, call Call) (string, string, error) {
	item, err := d.custom(ctx, call.OrganizationID, strings.TrimPrefix(call.Name, "custom__"))
	if err != nil || !item.Enabled {
		return "", "", task.ErrInvalid
	}
	if err := validateSchema(item.InputSchema, call.Input); err != nil {
		return "", "", err
	}
	var values map[string]any
	if err := json.Unmarshal(call.Input, &values); err != nil {
		return "", "", err
	}
	var invalid bool
	urlText := templateField.ReplaceAllStringFunc(item.URLTemplate, func(token string) string {
		key := strings.TrimSuffix(strings.TrimPrefix(token, "{{"), "}}")
		value, ok := values[key].(string)
		if !ok {
			invalid = true
			return ""
		}
		return url.QueryEscape(value)
	})
	if invalid {
		return "", "", task.ErrInvalid
	}
	return urlText, item.Method, nil
}

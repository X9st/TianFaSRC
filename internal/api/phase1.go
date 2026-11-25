package api

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/X9st/TianFaSRC/internal/agent"
	"github.com/X9st/TianFaSRC/internal/auth"
	"github.com/X9st/TianFaSRC/internal/llm"
	"github.com/X9st/TianFaSRC/internal/task"
)

const sessionCookie = "tianfa_session"
const csrfCookie = "tianfa_csrf"

type phase1API struct {
	auth   auth.Store
	tasks  task.Store
	engine *agent.Engine
}

func (api phase1API) routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/login", api.login)
	mux.HandleFunc("POST /api/v1/auth/logout", api.logout)
	mux.HandleFunc("GET /api/v1/me", api.me)
	mux.HandleFunc("GET /api/v1/users", api.listUsers)
	mux.HandleFunc("POST /api/v1/users", api.createUser)
	mux.HandleFunc("PATCH /api/v1/users/{id}", api.updateUser)
	mux.HandleFunc("GET /api/v1/tasks", api.listTasks)
	mux.HandleFunc("POST /api/v1/tasks", api.createTask)
	mux.HandleFunc("GET /api/v1/tasks/{id}", api.getTask)
	mux.HandleFunc("PATCH /api/v1/tasks/{id}", api.updateTask)
	mux.HandleFunc("POST /api/v1/tasks/{action}", func(w http.ResponseWriter, r *http.Request) {
		value := r.PathValue("action")
		if strings.HasSuffix(value, ":advance") && api.engine != nil {
			r.SetPathValue("id", strings.TrimSuffix(value, ":advance"))
			api.advanceTask(w, r)
			return
		}
		if !strings.HasSuffix(value, ":start") {
			writeProblem(w, r, 404, "route-not-found", "接口不存在", "请求的 API 路由尚未定义")
			return
		}
		r.SetPathValue("id", strings.TrimSuffix(value, ":start"))
		api.startTask(w, r)
	})
	mux.HandleFunc("PUT /api/v1/tasks/{id}/scope", api.replaceScope)
	mux.HandleFunc("POST /api/v1/tasks/{id}/scope:validate", api.validateScope)
	mux.HandleFunc("POST /api/v1/tasks/{id}/goals", api.addGoal)
	mux.HandleFunc("POST /api/v1/tasks/{id}/constraints", api.addConstraint)
	mux.HandleFunc("GET /api/v1/tasks/{id}/graph", api.getGraph)
	mux.HandleFunc("GET /api/v1/tasks/{id}/coverage", api.getCoverage)
	mux.HandleFunc("GET /api/v1/tasks/{id}/activities", api.getActivities)
	mux.HandleFunc("GET /api/v1/nodes/{id}", api.getNode)
	mux.HandleFunc("POST /api/v1/nodes/{id}/assets", api.anchorAsset)
	mux.HandleFunc("GET /api/v1/assets", api.listAssets)
	mux.HandleFunc("GET /api/v1/assets/graph", api.getAssetGraph)
	mux.HandleFunc("POST /api/v1/assets", api.upsertAsset)
	mux.HandleFunc("POST /api/v1/tasks/{id}/assets", api.linkAsset)
}

func (api phase1API) advanceTask(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	if err := api.engine.Advance(r.Context(), user.OrganizationID, user.ID, r.PathValue("id")); err != nil {
		apiError(w, r, err)
		return
	}
	item, err := api.tasks.Get(r.Context(), user.OrganizationID, r.PathValue("id"))
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, item)
}

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("multiple JSON values")
	}
	return nil
}

func writeData(w http.ResponseWriter, r *http.Request, status int, value any) {
	writeJSON(w, status, map[string]any{"data": value, "meta": map[string]any{"request_id": requestID(r.Context())}})
}

func (api phase1API) principal(w http.ResponseWriter, r *http.Request, roles ...string) (auth.Session, string, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		writeProblem(w, r, http.StatusUnauthorized, "unauthenticated", "未登录", "请先登录")
		return auth.Session{}, "", false
	}
	session, err := api.auth.FromToken(r.Context(), cookie.Value)
	if err != nil {
		if errors.Is(err, auth.ErrSessionExpired) {
			writeProblem(w, r, http.StatusUnauthorized, "session-expired", "会话已失效", "请重新登录")
		} else {
			writeProblem(w, r, 503, "database-unavailable", "依赖不可用", "数据库暂时不可用")
		}
		return auth.Session{}, "", false
	}
	if len(roles) > 0 {
		allowed := false
		for _, role := range roles {
			if role == session.Role {
				allowed = true
				break
			}
		}
		if !allowed {
			writeProblem(w, r, http.StatusForbidden, "forbidden", "权限不足", "当前角色无权执行此操作")
			return auth.Session{}, "", false
		}
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
		if !api.auth.CheckCSRF(r.Context(), cookie.Value, r.Header.Get("X-CSRF-Token")) {
			writeProblem(w, r, http.StatusForbidden, "csrf-invalid", "请求验证失败", "请刷新页面后重试")
			return auth.Session{}, "", false
		}
	}
	return session, cookie.Value, true
}

func (api phase1API) login(w http.ResponseWriter, r *http.Request) {
	if !secureRequest(r) && !loopbackHost(r.Host) {
		writeProblem(w, r, 403, "https-required", "需要 HTTPS", "请通过 HTTPS 访问控制台")
		return
	}
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	user, token, csrf, err := api.auth.Login(r.Context(), input.Email, input.Password)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		writeProblem(w, r, 401, "invalid-credentials", "登录失败", "邮箱或密码不正确")
		return
	}
	if err != nil {
		writeProblem(w, r, 503, "database-unavailable", "依赖不可用", "登录暂时不可用")
		return
	}
	secure := secureRequest(r)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", MaxAge: 8 * 60 * 60, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode})
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: csrf, Path: "/", MaxAge: 8 * 60 * 60, HttpOnly: false, Secure: secure, SameSite: http.SameSiteStrictMode})
	writeData(w, r, 200, user)
}

func (api phase1API) logout(w http.ResponseWriter, r *http.Request) {
	_, token, ok := api.principal(w, r)
	if !ok {
		return
	}
	if err := api.auth.Logout(r.Context(), token); err != nil {
		writeProblem(w, r, 503, "database-unavailable", "依赖不可用", "退出暂时不可用")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode})
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Path: "/", MaxAge: -1, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode})
	w.WriteHeader(204)
}

func (api phase1API) me(w http.ResponseWriter, r *http.Request) {
	session, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	writeData(w, r, 200, session.User)
}

func apiError(w http.ResponseWriter, r *http.Request, err error) {
	var blocked *llm.BlockedError
	if errors.As(err, &blocked) {
		writeProblem(w, r, 503, "model-blocked", "模型暂时不可用", blocked.Reason)
		return
	}
	var provider *llm.ProviderError
	if errors.As(err, &provider) {
		writeProblem(w, r, 502, "model-provider-error", "模型服务异常", provider.Class)
		return
	}
	if errors.Is(err, llm.ErrInvalid) {
		writeProblem(w, r, 502, "invalid-agent-output", "模型输出无效", "模型返回的数据不符合结构约束，请检查 Profile、提示模板和运行记录")
		return
	}
	if errors.Is(err, auth.ErrInvalidInput) {
		writeProblem(w, r, 422, "invalid-input", "输入无效", "请检查邮箱、角色及密码长度（至少 12 字节）")
		return
	}
	if errors.Is(err, auth.ErrLastAdmin) {
		writeProblem(w, r, 409, "last-admin", "不能移除最后一位管理员", "请先创建另一位可用管理员")
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "not-found", "资源不存在", "找不到请求的资源")
		return
	}
	if errors.Is(err, task.ErrNotFound) {
		writeProblem(w, r, 404, "not-found", "资源不存在", "找不到请求的资源")
		return
	}
	if errors.Is(err, task.ErrConflict) {
		writeProblem(w, r, 409, "version-conflict", "版本冲突", "资源已变化，请刷新后重试")
		return
	}
	if errors.Is(err, task.ErrModelUnavailable) {
		writeProblem(w, r, 409, "model-unavailable", "模型尚未配置", err.Error())
		return
	}
	if errors.Is(err, task.ErrInvalid) {
		writeProblem(w, r, 422, "invalid-input", "输入无效", err.Error())
		return
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "23505" {
		writeProblem(w, r, 409, "duplicate", "资源已存在", "请检查重复的名称或范围")
		return
	}
	if errors.As(err, &pgerr) && pgerr.Code == "22P02" {
		writeProblem(w, r, 400, "invalid-id", "标识格式错误", "请检查资源 ID 或分页游标")
		return
	}
	writeProblem(w, r, 503, "database-unavailable", "依赖不可用", "数据库暂时不可用")
}

func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
func loopbackHost(host string) bool {
	if value, _, err := net.SplitHostPort(host); err == nil {
		host = value
	}
	return host == "localhost" || net.ParseIP(host).IsLoopback()
}

func (api phase1API) updateTask(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	var input task.DraftPatch
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	item, err := api.tasks.UpdateDraft(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"), input)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, item)
}

func (api phase1API) startTask(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	var input struct {
		Version int64 `json:"version"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	var err error
	if api.engine != nil {
		err = api.engine.Start(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"), input.Version)
	} else {
		err = api.tasks.Start(r.Context(), user.OrganizationID, r.PathValue("id"), input.Version)
	}
	if err != nil {
		apiError(w, r, err)
		return
	}
	if api.engine != nil {
		item, err := api.tasks.Get(r.Context(), user.OrganizationID, r.PathValue("id"))
		if err != nil {
			apiError(w, r, err)
			return
		}
		writeData(w, r, 200, item)
		return
	}
}

func (api phase1API) replaceScope(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	var input struct {
		Version int64                  `json:"version"`
		Entries []task.ScopeEntryInput `json:"entries"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	item, err := api.tasks.ReplaceScope(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"), input.Version, input.Entries)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, item)
}

func (api phase1API) validateScope(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	if _, err := api.tasks.GetTaskOnly(r.Context(), user.OrganizationID, r.PathValue("id")); err != nil {
		apiError(w, r, err)
		return
	}
	var input struct {
		Entries []task.ScopeEntryInput `json:"entries"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	result, err := task.NormalizeScopeSet(input.Entries)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, result)
}

func (api phase1API) getActivities(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	items, err := api.tasks.ListActivities(r.Context(), user.OrganizationID, r.PathValue("id"), listLimit(r))
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}

func (api phase1API) getNode(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	item, err := api.tasks.GetNode(r.Context(), user.OrganizationID, r.PathValue("id"))
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, item)
}

func (api phase1API) addGoal(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	var input struct {
		Version  int64  `json:"version"`
		Text     string `json:"text"`
		Priority int    `json:"priority"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	result, err := api.tasks.AddGoal(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"), input.Version, task.GoalInput{Text: input.Text, Priority: input.Priority})
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 201, result)
}

func (api phase1API) addConstraint(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	var input struct {
		Version int64  `json:"version"`
		Effect  string `json:"effect"`
		Text    string `json:"text"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	result, err := api.tasks.AddConstraint(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"), input.Version, task.ConstraintInput{Effect: input.Effect, Text: input.Text})
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 201, result)
}

func (api phase1API) createTask(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	var input task.CreateInput
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	result, err := api.tasks.Create(r.Context(), user.OrganizationID, user.ID, input)
	if err != nil {
		apiError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/tasks/"+result.Task.ID)
	writeData(w, r, 201, result)
}

func (api phase1API) listTasks(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	items, err := api.tasks.List(r.Context(), user.OrganizationID, r.URL.Query().Get("cursor"), listLimit(r))
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}

func (api phase1API) getTask(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	item, err := api.tasks.Get(r.Context(), user.OrganizationID, r.PathValue("id"))
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, item)
}

func (api phase1API) getGraph(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	graph, err := api.tasks.Graph(r.Context(), user.OrganizationID, r.PathValue("id"))
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, graph)
}

func (api phase1API) getCoverage(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	coverage, err := api.tasks.Coverage(r.Context(), user.OrganizationID, r.PathValue("id"))
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, coverage)
}

func (api phase1API) upsertAsset(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	var input task.AssetInput
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	asset, err := api.tasks.UpsertAsset(r.Context(), user.OrganizationID, user.ID, input)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 201, asset)
}

func (api phase1API) listAssets(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	items, err := api.tasks.ListAssets(r.Context(), user.OrganizationID, r.URL.Query().Get("kind"), r.URL.Query().Get("cursor"), listLimit(r))
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}

func (api phase1API) getAssetGraph(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	graph, err := api.tasks.GraphAssets(r.Context(), user.OrganizationID)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, graph)
}

func (api phase1API) anchorAsset(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	var input struct {
		AssetID string `json:"asset_id"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	if err := api.tasks.AnchorAsset(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"), input.AssetID); err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, map[string]string{"asset_id": input.AssetID})
}

func (api phase1API) linkAsset(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	var input struct {
		AssetID string `json:"asset_id"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	if err := api.tasks.LinkAsset(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"), input.AssetID); err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, map[string]string{"asset_id": input.AssetID})
}

func listLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n < 1 || n > 100 {
		return 30
	}
	return n
}

func (api phase1API) listUsers(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	cursor := r.URL.Query().Get("cursor")
	if cursor == "" {
		cursor = "ffffffff-ffff-7fff-bfff-ffffffffffff"
	}
	rows, err := api.auth.Pool.Query(r.Context(), `SELECT id,organization_id,email,role,status,created_at FROM users WHERE organization_id=$1 AND id<$2 ORDER BY id DESC LIMIT $3`, user.OrganizationID, cursor, listLimit(r))
	if err != nil {
		apiError(w, r, err)
		return
	}
	defer rows.Close()
	items := []auth.User{}
	for rows.Next() {
		var item auth.User
		if err := rows.Scan(&item.ID, &item.OrganizationID, &item.Email, &item.Role, &item.Status, &item.CreatedAt); err != nil {
			apiError(w, r, err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}

func (api phase1API) createUser(w http.ResponseWriter, r *http.Request) {
	actor, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	user, err := api.auth.CreateUser(r.Context(), actor.OrganizationID, actor.ID, input.Email, input.Password, input.Role)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 201, user)
}

func (api phase1API) updateUser(w http.ResponseWriter, r *http.Request) {
	actor, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	var input struct {
		Role   *string `json:"role"`
		Status *string `json:"status"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	user, err := api.auth.UpdateUser(r.Context(), actor.OrganizationID, actor.ID, r.PathValue("id"), input.Role, input.Status)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, user)
}

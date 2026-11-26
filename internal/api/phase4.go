package api

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/X9st/TianFaSRC/internal/mcpclient"
	"github.com/X9st/TianFaSRC/internal/skill"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/X9st/TianFaSRC/internal/tool"
)

type phase4API struct{ phase1API }

func (api phase4API) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/tools", api.tools)
	mux.HandleFunc("PUT /api/v1/tools/{name}/binding", api.setToolBinding)
	mux.HandleFunc("GET /api/v1/tasks/{id}/tool-calls", api.toolCalls)
	mux.HandleFunc("GET /api/v1/tool-calls/{id}/output", api.toolOutput)
	mux.HandleFunc("GET /api/v1/approvals", api.approvals)
	mux.HandleFunc("POST /api/v1/approvals/{action}", api.decideApproval)
	mux.HandleFunc("GET /api/v1/skills", api.skills)
	mux.HandleFunc("POST /api/v1/skills", api.installSkill)
	mux.HandleFunc("PUT /api/v1/skills/{id}", api.setSkill)
	mux.HandleFunc("GET /api/v1/mcp-servers", api.mcpServers)
	mux.HandleFunc("POST /api/v1/mcp-servers", api.createMCPServer)
	mux.HandleFunc("PUT /api/v1/mcp-servers/{name}", api.setMCPServer)
	mux.HandleFunc("POST /api/v1/mcp-servers/{name}/discover", api.discoverMCP)
	mux.HandleFunc("GET /api/v1/mcp-tools", api.mcpTools)
	mux.HandleFunc("PUT /api/v1/mcp-servers/{name}/tools/{tool}", api.setMCPTool)
	mux.HandleFunc("GET /api/v1/custom-tools", api.customTools)
	mux.HandleFunc("POST /api/v1/custom-tools", api.createCustomTool)
	mux.HandleFunc("PUT /api/v1/custom-tools/{name}", api.setCustomTool)
}

func (api phase4API) dispatcher() *tool.Dispatcher { return api.scheduler.Tools }

func (api phase4API) tools(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	bindings, err := api.dispatcher().Bindings(r.Context(), user.OrganizationID)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, bindings)
}

func (api phase4API) setToolBinding(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	if err := api.dispatcher().SetBinding(r.Context(), user.OrganizationID, user.ID, r.PathValue("name"), input.Enabled); err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, map[string]any{"name": r.PathValue("name"), "enabled": input.Enabled})
}

func (api phase4API) toolCalls(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	if _, err := api.tasks.Get(r.Context(), user.OrganizationID, r.PathValue("id")); err != nil {
		apiError(w, r, err)
		return
	}
	items, err := api.dispatcher().ListCalls(r.Context(), user.OrganizationID, r.PathValue("id"))
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}

func (api phase4API) toolOutput(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	data, contentType, err := api.dispatcher().ReadOutput(r.Context(), user.OrganizationID, r.PathValue("id"))
	if err != nil {
		apiError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "attachment; filename=tool-output")
	w.WriteHeader(200)
	_, _ = w.Write(data)
}

func (api phase4API) approvals(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	items, err := api.dispatcher().ListApprovals(r.Context(), user.OrganizationID)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}

func (api phase4API) decideApproval(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	action := r.PathValue("action")
	approve := strings.HasSuffix(action, ":approve")
	reject := strings.HasSuffix(action, ":reject")
	if !approve && !reject {
		writeProblem(w, r, 404, "route-not-found", "接口不存在", "未知审批操作")
		return
	}
	id := strings.TrimSuffix(strings.TrimSuffix(action, ":approve"), ":reject")
	var input struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	if reject && strings.TrimSpace(input.Reason) == "" {
		apiError(w, r, task.ErrInvalid)
		return
	}
	if err := api.dispatcher().DecideApproval(r.Context(), user.OrganizationID, user.ID, id, approve, input.Reason); err != nil {
		if errors.Is(err, tool.ErrApprovalRequired) {
			writeProblem(w, r, 409, "approval-pending", "审批未完成", err.Error())
			return
		}
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, map[string]any{"id": id, "status": map[bool]string{true: "approved", false: "rejected"}[approve]})
}

func (api phase4API) skills(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	items, err := (skill.Store{Pool: api.tasks.Pool}).List(r.Context(), user.OrganizationID)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}

func (api phase4API) installSkill(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	if r.Header.Get("Content-Type") != "application/zip" {
		writeProblem(w, r, 415, "invalid-content-type", "文件类型错误", "请上传 ZIP 格式 Skill 包")
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		apiError(w, r, task.ErrInvalid)
		return
	}
	item, err := (skill.Store{Pool: api.tasks.Pool}).InstallZIP(r.Context(), user.OrganizationID, user.ID, data)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 201, item)
}

func (api phase4API) setSkill(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	if err := (skill.Store{Pool: api.tasks.Pool}).SetEnabled(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"), input.Enabled); err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, input)
}

func (api phase4API) mcpStore() mcpclient.Store {
	return mcpclient.Store{Pool: api.tasks.Pool, Enforcer: api.dispatcher().Enforcer}
}

func (api phase4API) mcpServers(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	items, err := api.mcpStore().List(r.Context(), user.OrganizationID)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}

func (api phase4API) createMCPServer(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	var input mcpclient.Server
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	item, err := api.mcpStore().Create(r.Context(), user.OrganizationID, user.ID, input)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 201, item)
}

func (api phase4API) setMCPServer(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	if err := api.mcpStore().SetEnabled(r.Context(), user.OrganizationID, user.ID, r.PathValue("name"), input.Enabled); err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, input)
}

func (api phase4API) discoverMCP(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	items, err := api.mcpStore().Discover(r.Context(), user.OrganizationID, r.PathValue("name"))
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}

func (api phase4API) mcpTools(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	items, err := api.mcpStore().ListTools(r.Context(), user.OrganizationID)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}

func (api phase4API) setMCPTool(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	if err := api.mcpStore().SetToolEnabled(r.Context(), user.OrganizationID, user.ID, r.PathValue("name"), r.PathValue("tool"), input.Enabled); err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, input)
}

func (api phase4API) customTools(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	items, err := api.dispatcher().ListCustom(r.Context(), user.OrganizationID)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}

func (api phase4API) createCustomTool(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	var input tool.CustomHTTP
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	item, err := api.dispatcher().CreateCustom(r.Context(), user.OrganizationID, user.ID, input)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 201, item)
}

func (api phase4API) setCustomTool(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	if err := api.dispatcher().SetCustomEnabled(r.Context(), user.OrganizationID, user.ID, r.PathValue("name"), input.Enabled); err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, input)
}

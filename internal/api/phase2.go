package api

import (
	"errors"
	"net/http"

	"github.com/X9st/TianFaSRC/internal/llm"
	"github.com/jackc/pgx/v5/pgconn"
)

type phase2API struct {
	phase1API
	profiles llm.Store
}

func (api phase2API) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/llm-profiles", api.listProfiles)
	mux.HandleFunc("POST /api/v1/llm-profiles", api.createProfile)
	mux.HandleFunc("GET /api/v1/llm-profiles/{id}", api.getProfile)
	mux.HandleFunc("PATCH /api/v1/llm-profiles/{id}", api.updateProfile)
	mux.HandleFunc("DELETE /api/v1/llm-profiles/{id}", api.deleteProfile)
	mux.HandleFunc("GET /api/v1/llm-default-chain", api.getDefaultChain)
	mux.HandleFunc("PUT /api/v1/llm-default-chain", api.setDefaultChain)
	mux.HandleFunc("GET /api/v1/tasks/{id}/llm-chain", api.getTaskChain)
	mux.HandleFunc("PUT /api/v1/tasks/{id}/llm-chain", api.setTaskChain)
	mux.HandleFunc("GET /api/v1/tasks/{id}/agent-routes/{role}", api.getAgentRoute)
	mux.HandleFunc("PUT /api/v1/tasks/{id}/agent-routes/{role}", api.setAgentRoute)
	mux.HandleFunc("GET /api/v1/tasks/{id}/intents", api.listIntents)
	mux.HandleFunc("GET /api/v1/tasks/{id}/agent-runs", api.listRuns)
	mux.HandleFunc("GET /api/v1/agent-runs/{id}", api.getRun)
	mux.HandleFunc("GET /api/v1/agent-prompts/{role}", api.getPrompt)
	mux.HandleFunc("PUT /api/v1/agent-prompts/{role}", api.updatePrompt)
}

func phase2Error(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, llm.ErrInvalid) {
		writeProblem(w, r, 422, "invalid-profile", "配置无效", "请检查格式、模型、地址、额度；角色 Profile 必须属于任务主备链")
		return
	}
	if errors.Is(err, llm.ErrCredentialRequired) {
		writeProblem(w, r, 422, "credential-required", "需要新凭据", "更换服务主机时请重新填写 API Key")
		return
	}
	if errors.Is(err, llm.ErrNotFound) {
		writeProblem(w, r, 404, "not-found", "资源不存在", "找不到请求的配置")
		return
	}
	if errors.Is(err, llm.ErrConflict) {
		writeProblem(w, r, 409, "profile-in-use", "配置正在使用", "此配置已被主备链、任务或历史运行引用；可以先停用")
		return
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "23505" {
		writeProblem(w, r, 409, "duplicate", "名称已存在", "请使用不同的配置名称")
		return
	}
	apiError(w, r, err)
}

func (api phase2API) listProfiles(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	items, err := api.profiles.List(r.Context(), user.OrganizationID)
	if err != nil {
		phase2Error(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}
func (api phase2API) getProfile(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	item, err := api.profiles.Get(r.Context(), user.OrganizationID, r.PathValue("id"))
	if err != nil {
		phase2Error(w, r, err)
		return
	}
	writeData(w, r, 200, item)
}
func (api phase2API) createProfile(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	var input llm.CreateInput
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	item, err := api.profiles.Create(r.Context(), user.OrganizationID, user.ID, input)
	if err != nil {
		phase2Error(w, r, err)
		return
	}
	writeData(w, r, 201, item)
}
func (api phase2API) updateProfile(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	var input llm.PatchInput
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	item, err := api.profiles.Update(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"), input)
	if err != nil {
		phase2Error(w, r, err)
		return
	}
	writeData(w, r, 200, item)
}
func (api phase2API) deleteProfile(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	if err := api.profiles.Delete(r.Context(), user.OrganizationID, user.ID, r.PathValue("id")); err != nil {
		phase2Error(w, r, err)
		return
	}
	w.WriteHeader(204)
}

type chainInput struct {
	Profiles []string `json:"profiles"`
}

func (api phase2API) getDefaultChain(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	ids, err := api.profiles.GetDefaultChain(r.Context(), user.OrganizationID)
	if err != nil {
		phase2Error(w, r, err)
		return
	}
	writeData(w, r, 200, chainInput{Profiles: ids})
}
func (api phase2API) setDefaultChain(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	var input chainInput
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	if err := api.profiles.SetDefaultChain(r.Context(), user.OrganizationID, user.ID, input.Profiles); err != nil {
		phase2Error(w, r, err)
		return
	}
	writeData(w, r, 200, input)
}
func (api phase2API) getTaskChain(w http.ResponseWriter, r *http.Request) {
	api.readTaskChain(w, r, "")
}
func (api phase2API) getAgentRoute(w http.ResponseWriter, r *http.Request) {
	api.readTaskChain(w, r, r.PathValue("role"))
}
func (api phase2API) readTaskChain(w http.ResponseWriter, r *http.Request, role string) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	ids, err := api.profiles.GetTaskChain(r.Context(), user.OrganizationID, r.PathValue("id"), role)
	if err != nil {
		phase2Error(w, r, err)
		return
	}
	writeData(w, r, 200, chainInput{Profiles: ids})
}
func (api phase2API) setTaskChain(w http.ResponseWriter, r *http.Request) {
	api.writeTaskChain(w, r, "")
}
func (api phase2API) setAgentRoute(w http.ResponseWriter, r *http.Request) {
	api.writeTaskChain(w, r, r.PathValue("role"))
}
func (api phase2API) writeTaskChain(w http.ResponseWriter, r *http.Request, role string) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	var input chainInput
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	if err := api.profiles.SetTaskChain(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"), role, input.Profiles); err != nil {
		phase2Error(w, r, err)
		return
	}
	writeData(w, r, 200, input)
}

func (api phase2API) listIntents(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	items, err := api.engine.ListIntents(r.Context(), user.OrganizationID, r.PathValue("id"))
	if err != nil {
		phase2Error(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}
func (api phase2API) listRuns(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	items, err := api.engine.ListRuns(r.Context(), user.OrganizationID, r.PathValue("id"))
	if err != nil {
		phase2Error(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}

func (api phase2API) getRun(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	item, err := api.engine.GetRun(r.Context(), user.OrganizationID, r.PathValue("id"))
	if err != nil {
		phase2Error(w, r, err)
		return
	}
	writeData(w, r, 200, item)
}
func (api phase2API) getPrompt(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	item, err := api.profiles.ActivePrompt(r.Context(), user.OrganizationID, user.ID, r.PathValue("role"))
	if err != nil {
		phase2Error(w, r, err)
		return
	}
	writeData(w, r, 200, item)
}
func (api phase2API) updatePrompt(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin")
	if !ok {
		return
	}
	var input struct {
		Body string `json:"body"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	item, err := api.profiles.UpdatePrompt(r.Context(), user.OrganizationID, user.ID, r.PathValue("role"), input.Body)
	if err != nil {
		phase2Error(w, r, err)
		return
	}
	writeData(w, r, 200, item)
}

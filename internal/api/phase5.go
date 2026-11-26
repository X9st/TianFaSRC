package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/X9st/TianFaSRC/internal/blob"
	"github.com/X9st/TianFaSRC/internal/finding"
	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/recordproxy"
	"github.com/X9st/TianFaSRC/internal/report"
	"github.com/X9st/TianFaSRC/internal/task"
	"github.com/X9st/TianFaSRC/internal/traffic"
)

type phase5API struct {
	phase1API
	caCertificate []byte
}

func newID() string { return identity.NewID() }

func (api phase5API) trafficStore() traffic.Store {
	return traffic.Store{Pool: api.tasks.Pool, Blobs: blob.Store{Root: api.scheduler.Tools.DataDir}}
}
func (api phase5API) findingStore() finding.Store {
	return finding.Store{Pool: api.tasks.Pool, Blobs: blob.Store{Root: api.scheduler.Tools.DataDir}}
}
func (api phase5API) reportStore() report.Store {
	return report.Store{Pool: api.tasks.Pool, Blobs: blob.Store{Root: api.scheduler.Tools.DataDir}}
}

func (api phase5API) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/traffic", api.listTraffic)
	mux.HandleFunc("GET /api/v1/traffic/{id}", api.getTraffic)
	mux.HandleFunc("GET /api/v1/traffic/{id}/body", api.trafficBody)
	mux.HandleFunc("DELETE /api/v1/traffic/{id}", api.deleteTraffic)
	mux.HandleFunc("GET /api/v1/findings", api.listFindings)
	mux.HandleFunc("POST /api/v1/findings", api.createFinding)
	mux.HandleFunc("GET /api/v1/findings/{id}", api.getFinding)
	mux.HandleFunc("POST /api/v1/findings/{action}", api.findingAction)
	mux.HandleFunc("GET /api/v1/findings/{id}/evidence", api.listEvidence)
	mux.HandleFunc("POST /api/v1/findings/{id}/evidence", api.bindEvidence)
	mux.HandleFunc("GET /api/v1/evidence-bindings/{id}/body", api.evidenceBody)
	mux.HandleFunc("DELETE /api/v1/evidence-bindings/{id}", api.unbindEvidence)
	mux.HandleFunc("PATCH /api/v1/evidence-bindings/{id}", api.updateEvidenceBinding)
	mux.HandleFunc("PUT /api/v1/findings/{id}/evidence-order", api.reorderEvidence)
	mux.HandleFunc("GET /api/v1/tasks/{id}/report", api.getReport)
	mux.HandleFunc("PUT /api/v1/tasks/{id}/report", api.updateReport)
	mux.HandleFunc("GET /api/v1/tasks/{id}/export", api.exportReport)
	mux.HandleFunc("GET /api/v1/findings/{id}/retests", api.listRetests)
	mux.HandleFunc("POST /api/v1/findings/{id}/retests", api.createRetest)
	mux.HandleFunc("GET /api/v1/retests/{id}", api.getRetest)
	mux.HandleFunc("POST /api/v1/retests/{action}", api.retestAction)
	mux.HandleFunc("POST /api/v1/tasks/{id}/proxy-token", api.issueProxyToken)
	mux.HandleFunc("GET /api/v1/proxy/ca", api.downloadCA)
}

func (api phase5API) listTraffic(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	status, err := strconv.Atoi(query.Get("status_code"))
	if query.Get("status_code") != "" && err != nil {
		apiError(w, r, task.ErrInvalid)
		return
	}
	limit, err := strconv.Atoi(query.Get("limit"))
	if query.Get("limit") != "" && err != nil {
		apiError(w, r, task.ErrInvalid)
		return
	}
	filter := traffic.Filter{TaskID: query.Get("task_id"), Host: query.Get("host"), Path: query.Get("path"), BodyKeyword: query.Get("body_keyword"), StatusCode: status, Cursor: query.Get("cursor"), Limit: limit}
	if query.Get("after") != "" {
		value, err := time.Parse(time.RFC3339, query.Get("after"))
		if err != nil {
			apiError(w, r, task.ErrInvalid)
			return
		}
		filter.After = &value
	}
	if query.Get("before") != "" {
		value, err := time.Parse(time.RFC3339, query.Get("before"))
		if err != nil {
			apiError(w, r, task.ErrInvalid)
			return
		}
		filter.Before = &value
	}
	if query.Get("min_response_bytes") != "" {
		value, err := strconv.ParseInt(query.Get("min_response_bytes"), 10, 64)
		if err != nil || value < 0 {
			apiError(w, r, task.ErrInvalid)
			return
		}
		filter.MinResponseBytes = value
	}
	if query.Get("max_response_bytes") != "" {
		value, err := strconv.ParseInt(query.Get("max_response_bytes"), 10, 64)
		if err != nil || value < 0 {
			apiError(w, r, task.ErrInvalid)
			return
		}
		filter.MaxResponseBytes = value
	}
	items, err := api.trafficStore().List(r.Context(), user.OrganizationID, filter)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}

func (api phase5API) getTraffic(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	reveal := r.URL.Query().Get("reveal") == "1"
	if reveal && user.Role != "reviewer" && user.Role != "admin" {
		writeProblem(w, r, 403, "forbidden", "权限不足", "查看原始敏感 Header 需要 Reviewer 或 Admin 角色")
		return
	}
	item, err := api.trafficStore().Get(r.Context(), user.OrganizationID, r.PathValue("id"), reveal)
	if err != nil {
		apiError(w, r, err)
		return
	}
	if reveal {
		if _, err := api.tasks.Pool.Exec(r.Context(), `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id) VALUES($1,$2,$3,'traffic.revealed','traffic',$4)`, newID(), user.OrganizationID, user.ID, item.ID); err != nil {
			apiError(w, r, err)
			return
		}
	}
	writeData(w, r, 200, item)
}

func (api phase5API) trafficBody(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "reviewer", "admin")
	if !ok {
		return
	}
	part := r.URL.Query().Get("part")
	data, err := api.trafficStore().ReadBody(r.Context(), user.OrganizationID, r.PathValue("id"), part)
	if err != nil {
		apiError(w, r, err)
		return
	}
	if _, err := api.tasks.Pool.Exec(r.Context(), `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,$3,'traffic.body_read','traffic',$4,$5)`, newID(), user.OrganizationID, user.ID, r.PathValue("id"), map[string]any{"part": part}); err != nil {
		apiError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = w.Write(data)
}

func (api phase5API) deleteTraffic(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	if err := api.trafficStore().Delete(r.Context(), user.OrganizationID, r.PathValue("id"), user.ID); err != nil {
		apiError(w, r, err)
		return
	}
	w.WriteHeader(204)
}

func (api phase5API) listFindings(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	items, err := api.findingStore().List(r.Context(), user.OrganizationID, r.URL.Query().Get("task_id"))
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}
func (api phase5API) getFinding(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	item, err := api.findingStore().Get(r.Context(), user.OrganizationID, r.PathValue("id"))
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, item)
}
func (api phase5API) createFinding(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	var input finding.Input
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	item, err := api.findingStore().Create(r.Context(), user.OrganizationID, user.ID, input)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 201, item)
}
func (api phase5API) findingAction(w http.ResponseWriter, r *http.Request) {
	value := r.PathValue("action")
	if !strings.HasSuffix(value, ":confirm") {
		writeProblem(w, r, 404, "route-not-found", "接口不存在", "未知 Finding 操作")
		return
	}
	user, _, ok := api.principal(w, r, "admin", "reviewer")
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
	item, err := api.findingStore().Confirm(r.Context(), user.OrganizationID, user.ID, strings.TrimSuffix(value, ":confirm"), input.Version)
	if errors.Is(err, finding.ErrVersionConflict) {
		writeProblem(w, r, 409, "version-conflict", "版本冲突", err.Error())
		return
	}
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, item)
}
func (api phase5API) listEvidence(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	items, err := api.findingStore().Evidence(r.Context(), user.OrganizationID, r.PathValue("id"))
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}
func (api phase5API) bindEvidence(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator", "reviewer")
	if !ok {
		return
	}
	var input struct {
		TrafficID string `json:"traffic_id"`
		Role      string `json:"role"`
		Note      string `json:"note"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	item, err := api.findingStore().BindTraffic(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"), input.TrafficID, input.Role, input.Note)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 201, item)
}
func (api phase5API) evidenceBody(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "reviewer", "admin")
	if !ok {
		return
	}
	part := r.URL.Query().Get("part")
	data, err := api.findingStore().ReadEvidence(r.Context(), user.OrganizationID, r.PathValue("id"), part)
	if err != nil {
		apiError(w, r, err)
		return
	}
	if _, err := api.tasks.Pool.Exec(r.Context(), `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,$3,'evidence.body_read','evidence_binding',$4,$5)`, newID(), user.OrganizationID, user.ID, r.PathValue("id"), map[string]any{"part": part}); err != nil {
		apiError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = w.Write(data)
}
func (api phase5API) unbindEvidence(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "reviewer")
	if !ok {
		return
	}
	if err := api.findingStore().Unbind(r.Context(), user.OrganizationID, user.ID, r.PathValue("id")); err != nil {
		apiError(w, r, err)
		return
	}
	w.WriteHeader(204)
}

func (api phase5API) updateEvidenceBinding(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "reviewer")
	if !ok {
		return
	}
	var input struct {
		Role string `json:"role"`
		Note string `json:"note"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	item, err := api.findingStore().UpdateBinding(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"), input.Role, input.Note)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, item)
}

func (api phase5API) reorderEvidence(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "reviewer")
	if !ok {
		return
	}
	var input struct {
		BindingIDs []string `json:"binding_ids"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	items, err := api.findingStore().Reorder(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"), input.BindingIDs)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}

func (api phase5API) getReport(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	item, err := api.reportStore().Get(r.Context(), user.OrganizationID, r.PathValue("id"))
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, item)
}

func (api phase5API) updateReport(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "reviewer")
	if !ok {
		return
	}
	var input struct {
		Body            string `json:"body"`
		Version         int64  `json:"version"`
		EvidenceVersion int64  `json:"evidence_version"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	item, err := api.reportStore().Update(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"), input.Body, input.Version, input.EvidenceVersion)
	if errors.Is(err, report.ErrVersionConflict) {
		writeProblem(w, r, 409, "version-conflict", "版本冲突", err.Error())
		return
	}
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, item)
}

func (api phase5API) exportReport(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "reviewer", "operator")
	if !ok {
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "markdown"
	}
	data, contentType, err := api.reportStore().Export(r.Context(), user.OrganizationID, r.PathValue("id"), format)
	if err != nil {
		apiError(w, r, err)
		return
	}
	if _, err := api.tasks.Pool.Exec(r.Context(), `INSERT INTO audit_log(id,organization_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,$3,'report.exported','task',$4,$5)`, newID(), user.OrganizationID, user.ID, r.PathValue("id"), map[string]any{"format": format}); err != nil {
		apiError(w, r, err)
		return
	}
	ext := "md"
	if format == "json" {
		ext = "json"
	} else if format == "md-zip" {
		ext = "zip"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "attachment; filename=tianfa-report-"+r.PathValue("id")+"."+ext)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = w.Write(data)
}

func (api phase5API) listRetests(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	items, err := api.findingStore().ListRetests(r.Context(), user.OrganizationID, r.PathValue("id"))
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}
func (api phase5API) createRetest(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator", "reviewer")
	if !ok {
		return
	}
	item, err := api.findingStore().CreateRetest(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"))
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 201, item)
}
func (api phase5API) getRetest(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	item, err := api.findingStore().GetRetest(r.Context(), user.OrganizationID, r.PathValue("id"))
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, item)
}
func (api phase5API) retestAction(w http.ResponseWriter, r *http.Request) {
	value := r.PathValue("action")
	if !strings.HasSuffix(value, ":complete") {
		writeProblem(w, r, 404, "route-not-found", "接口不存在", "未知复测操作")
		return
	}
	user, _, ok := api.principal(w, r, "admin", "reviewer")
	if !ok {
		return
	}
	var input struct {
		Conclusion string   `json:"conclusion"`
		Note       string   `json:"note"`
		BindingIDs []string `json:"binding_ids"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	item, err := api.findingStore().CompleteRetest(r.Context(), user.OrganizationID, user.ID, strings.TrimSuffix(value, ":complete"), input.Conclusion, input.Note, input.BindingIDs)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, item)
}

func (api phase5API) issueProxyToken(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	token, err := (recordproxy.TokenStore{Pool: api.tasks.Pool}).Issue(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"))
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 201, token)
}

func (api phase5API) downloadCA(w http.ResponseWriter, r *http.Request) {
	_, _, ok := api.principal(w, r, "admin", "operator", "reviewer")
	if !ok {
		return
	}
	if len(api.caCertificate) == 0 {
		writeProblem(w, r, 503, "proxy-unavailable", "代理未就绪", "记录代理 CA 未初始化")
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", "attachment; filename=tianfa-recording-ca.crt")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = w.Write(api.caCertificate)
}

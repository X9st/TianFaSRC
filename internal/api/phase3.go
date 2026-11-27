package api

import (
	"encoding/json"
	"fmt"
	"github.com/X9st/TianFaSRC/internal/agent"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type phase3API struct{ phase1API }

func (api phase3API) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/tasks/{id}/todos", api.listTodos)
	mux.HandleFunc("POST /api/v1/tasks/{id}/todos", api.addTodo)
	mux.HandleFunc("POST /api/v1/tasks/{id}/todos/{action}", func(w http.ResponseWriter, r *http.Request) {
		value := r.PathValue("action")
		if !strings.HasSuffix(value, ":complete") {
			writeProblem(w, r, 404, "route-not-found", "接口不存在", "操作不存在")
			return
		}
		r.SetPathValue("todo", strings.TrimSuffix(value, ":complete"))
		api.completeTodo(w, r)
	})
	mux.HandleFunc("POST /api/v1/tasks/{id}/intents/{action}", func(w http.ResponseWriter, r *http.Request) {
		value := r.PathValue("action")
		for _, name := range []string{"steer", "stop", "retry"} {
			if strings.HasSuffix(value, ":"+name) {
				r.SetPathValue("intent", strings.TrimSuffix(value, ":"+name))
				switch name {
				case "steer":
					api.steer(w, r)
				case "stop":
					api.stopIntent(w, r)
				case "retry":
					api.retryIntent(w, r)
				}
				return
			}
		}
		writeProblem(w, r, 404, "route-not-found", "接口不存在", "操作不存在")
	})
	mux.HandleFunc("GET /api/v1/tasks/{id}/worker-traces", api.searchTraces)
	mux.HandleFunc("GET /api/v1/tasks/{id}/worker-budgets", api.listWorkerBudgets)
	mux.HandleFunc("GET /api/v1/tasks/{id}/events", api.streamTaskEvents)
}

func (api phase3API) streamTaskEvents(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	if _, err := api.tasks.Get(r.Context(), user.OrganizationID, r.PathValue("id")); err != nil {
		apiError(w, r, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeProblem(w, r, 500, "stream-unavailable", "事件流不可用", "服务端不支持流式输出")
		return
	}
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = r.URL.Query().Get("after")
	}
	var after time.Time
	var afterID string
	if cursor != "" {
		err := api.tasks.Pool.QueryRow(r.Context(), `SELECT occurred_at,id FROM domain_events WHERE id=$1 AND task_id=$2 AND organization_id=$3`, cursor, r.PathValue("id"), user.OrganizationID).Scan(&after, &afterID)
		if err != nil {
			writeProblem(w, r, 400, "invalid-cursor", "事件位置无效", "请刷新任务页面")
			return
		}
	} else {
		after = time.Now()
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
	for {
		rows, err := api.tasks.Pool.Query(r.Context(), `SELECT id,event_type,occurred_at FROM domain_events WHERE task_id=$1 AND organization_id=$2 AND (occurred_at,id)>($3,$4::uuid) ORDER BY occurred_at,id LIMIT 100`, r.PathValue("id"), user.OrganizationID, after, coalesceUUID(afterID))
		if err != nil {
			return
		}
		for rows.Next() {
			var id, kind string
			var at time.Time
			if rows.Scan(&id, &kind, &at) != nil {
				rows.Close()
				return
			}
			payload, _ := json.Marshal(map[string]string{"id": id, "kind": kind})
			fmt.Fprintf(w, "id: %s\nevent: change\ndata: %s\n\n", id, payload)
			after, afterID = at, id
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return
		}
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-timeout.C:
			return
		case <-ticker.C:
		}
	}
}

func coalesceUUID(value string) string {
	if value == "" {
		return "00000000-0000-0000-0000-000000000000"
	}
	return value
}

func (api phase3API) listWorkerBudgets(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	if _, err := api.tasks.Get(r.Context(), user.OrganizationID, r.PathValue("id")); err != nil {
		apiError(w, r, err)
		return
	}
	items, err := agent.ListWorkerBudgets(r.Context(), api.scheduler.Engine.Pool, user.OrganizationID, r.PathValue("id"))
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}

func (api phase1API) controlTask(w http.ResponseWriter, r *http.Request, command string) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	if err := api.scheduler.TaskCommand(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"), command); err != nil {
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

func (api phase3API) listTodos(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	items, err := api.scheduler.ListTodos(r.Context(), user.OrganizationID, r.PathValue("id"))
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}
func (api phase3API) addTodo(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	var input struct {
		Title     string   `json:"title"`
		Detail    string   `json:"detail"`
		DependsOn []string `json:"depends_on"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	item, err := api.scheduler.AddTodo(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"), input.Title, input.Detail, input.DependsOn)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 201, item)
}
func (api phase3API) completeTodo(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	if err := api.scheduler.CompleteTodo(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"), r.PathValue("todo")); err != nil {
		apiError(w, r, err)
		return
	}
	items, err := api.scheduler.ListTodos(r.Context(), user.OrganizationID, r.PathValue("id"))
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}
func (api phase3API) steer(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	var input struct {
		Message string `json:"message"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	if err := api.scheduler.Steer(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"), r.PathValue("intent"), input.Message); err != nil {
		apiError(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (api phase3API) stopIntent(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeProblem(w, r, 400, "invalid-json", "请求格式错误", err.Error())
		return
	}
	if err := api.scheduler.StopIntent(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"), r.PathValue("intent"), input.Reason); err != nil {
		apiError(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (api phase3API) retryIntent(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r, "admin", "operator")
	if !ok {
		return
	}
	if err := api.scheduler.RetryIntent(r.Context(), user.OrganizationID, user.ID, r.PathValue("id"), r.PathValue("intent")); err != nil {
		apiError(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (api phase3API) searchTraces(w http.ResponseWriter, r *http.Request) {
	user, _, ok := api.principal(w, r)
	if !ok {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 200 {
		writeProblem(w, r, 422, "invalid-query", "查询无效", "关键词过长")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 30
	}
	items, err := api.scheduler.SearchTraces(r.Context(), user.OrganizationID, r.PathValue("id"), query, r.URL.Query().Get("exclude_run_id"), limit)
	if err != nil {
		apiError(w, r, err)
		return
	}
	writeData(w, r, 200, items)
}

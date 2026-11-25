package api

func addPhase3OpenAPI(spec map[string]any) {
	paths := spec["paths"].(map[string]any)
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	str := map[string]any{"type": "string"}
	id := map[string]any{"type": "string", "format": "uuid"}
	arr := func(item any) map[string]any { return map[string]any{"type": "array", "items": item} }
	obj := func(required []string, props map[string]any) map[string]any {
		return map[string]any{"type": "object", "required": required, "properties": props}
	}
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	schemas["Todo"] = obj([]string{"id", "title", "status", "depends_on", "created_at"}, map[string]any{"id": id, "title": str, "detail": str, "status": str, "depends_on": arr(id), "intent_id": id, "created_at": map[string]any{"type": "string", "format": "date-time"}})
	schemas["TodoWrite"] = obj([]string{"title"}, map[string]any{"title": str, "detail": str, "depends_on": arr(id)})
	schemas["TraceHit"] = obj([]string{"run_id", "intent_id", "ordinal", "role", "excerpt", "created_at"}, map[string]any{"run_id": id, "intent_id": id, "ordinal": map[string]any{"type": "integer"}, "role": str, "excerpt": str, "created_at": map[string]any{"type": "string", "format": "date-time"}})
	problem := map[string]any{"description": "错误", "content": map[string]any{"application/problem+json": map[string]any{"schema": ref("Problem")}}}
	response := func(schema any) map[string]any {
		return map[string]any{"description": "成功", "content": map[string]any{"application/json": map[string]any{"schema": obj([]string{"data", "meta"}, map[string]any{"data": schema, "meta": obj([]string{"request_id"}, map[string]any{"request_id": str})})}}}
	}
	pathParam := func(name string) map[string]any {
		return map[string]any{"name": name, "in": "path", "required": true, "schema": id}
	}
	add := func(path, method, operation, summary string, result, request any, params []any) {
		op := map[string]any{"operationId": operation, "summary": summary, "security": []any{map[string]any{"SessionCookie": []any{}}}, "responses": map[string]any{"200": response(result), "400": problem, "401": problem, "403": problem, "404": problem, "409": problem, "422": problem, "503": problem}}
		if method != "get" {
			params = append(params, map[string]any{"name": "X-CSRF-Token", "in": "header", "required": true, "schema": str})
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		if request != nil {
			op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": request}}}
		}
		item, ok := paths[path].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[path] = item
		}
		item[method] = op
	}
	for _, cmd := range []string{"pause", "resume", "cancel"} {
		add("/api/v1/tasks/{id}:"+cmd, "post", cmd+"Task", "控制任务状态", ref("TaskDetail"), nil, []any{pathParam("id")})
	}
	add("/api/v1/tasks/{id}/todos", "get", "listTaskTodos", "列出共享步骤", arr(ref("Todo")), nil, []any{pathParam("id")})
	add("/api/v1/tasks/{id}/todos", "post", "createTaskTodo", "创建共享步骤", ref("Todo"), ref("TodoWrite"), []any{pathParam("id")})
	createResponses := paths["/api/v1/tasks/{id}/todos"].(map[string]any)["post"].(map[string]any)["responses"].(map[string]any)
	createResponses["201"] = createResponses["200"]
	delete(createResponses, "200")
	add("/api/v1/tasks/{id}/todos/{todo}:complete", "post", "completeTaskTodo", "完成步骤", arr(ref("Todo")), nil, []any{pathParam("id"), pathParam("todo")})
	for _, cmd := range []string{"steer", "stop", "retry"} {
		var request any
		if cmd == "steer" {
			request = obj([]string{"message"}, map[string]any{"message": str})
		} else if cmd == "stop" {
			request = obj([]string{"reason"}, map[string]any{"reason": str})
		}
		add("/api/v1/tasks/{id}/intents/{intent}:"+cmd, "post", cmd+"Intent", "控制执行方向", obj(nil, nil), request, []any{pathParam("id"), pathParam("intent")})
		op := paths["/api/v1/tasks/{id}/intents/{intent}:"+cmd].(map[string]any)["post"].(map[string]any)
		responses := op["responses"].(map[string]any)
		delete(responses, "200")
		responses["204"] = map[string]any{"description": "操作已接受"}
	}
	add("/api/v1/tasks/{id}/worker-traces", "get", "searchWorkerTraces", "检索本任务执行记录", arr(ref("TraceHit")), nil, []any{pathParam("id"), map[string]any{"name": "q", "in": "query", "schema": str}, map[string]any{"name": "exclude_run_id", "in": "query", "schema": id}, map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer"}}})
}

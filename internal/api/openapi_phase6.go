package api

func addPhase6OpenAPI(spec map[string]any) {
	paths := spec["paths"].(map[string]any)
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	str := map[string]any{"type": "string"}
	id := map[string]any{"type": "string", "format": "uuid"}
	num := map[string]any{"type": "integer"}
	date := map[string]any{"type": "string", "format": "date-time"}
	ref := func(name string) any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	obj := func(properties map[string]any) any { return map[string]any{"type": "object", "properties": properties} }
	arr := func(item any) any { return map[string]any{"type": "array", "items": item} }
	schemas["AuditEntry"] = obj(map[string]any{"id": id, "action": str, "resource_type": str, "resource_id": id, "actor": str, "occurred_at": date})
	schemas["PolicyBlock"] = obj(map[string]any{"id": id, "intent_id": id, "tool": str, "reason": str, "summary": str, "created_at": date})
	schemas["WorkspaceFile"] = obj(map[string]any{"intent_id": id, "name": str, "size": num, "modified_at": date})
	schemas["ConversationMessage"] = obj(map[string]any{"id": id, "role": str, "action": str, "content": str, "created_at": date})
	schemas["ConversationWrite"] = obj(map[string]any{"action": map[string]any{"type": "string", "enum": []string{"ask", "goal", "constraint", "intent", "steer"}}, "text": str, "goal_id": id, "intent_id": id, "success_criteria": str})
	response := func(data any) any {
		envelope := obj(map[string]any{"data": data, "meta": obj(map[string]any{"request_id": str})})
		return map[string]any{"description": "成功", "content": map[string]any{"application/json": map[string]any{"schema": envelope}}}
	}
	add := func(path, method, operation, summary string, data any, params []any, request any) {
		op := map[string]any{"operationId": operation, "summary": summary, "security": []any{map[string]any{"SessionCookie": []any{}}}, "responses": map[string]any{"200": response(data)}}
		if len(params) > 0 {
			op["parameters"] = params
		}
		if request != nil {
			op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": request}}}
		}
		p, ok := paths[path].(map[string]any)
		if !ok {
			p = map[string]any{}
			paths[path] = p
		}
		p[method] = op
	}
	taskID := map[string]any{"name": "id", "in": "path", "required": true, "schema": id}
	add("/api/v1/audit-log", "get", "listAuditLog", "分页查看本组织审计", obj(map[string]any{"items": arr(ref("AuditEntry")), "next_cursor": str}), nil, nil)
	add("/api/v1/dashboard", "get", "getDashboard", "查看漏洞挖掘工作台指标", obj(map[string]any{"active_workers": num, "input_tokens": num, "output_tokens": num, "cost_minor": num, "findings": obj(nil), "finding_statuses": obj(nil), "review_queue": arr(obj(nil)), "models": obj(nil), "recent_activity": arr(obj(nil))}), nil, nil)
	add("/api/v1/policy", "get", "getPolicyOverview", "查看运行硬边界", obj(map[string]any{"global_worker_limit": num, "blocked_tool_calls": num, "worker_hard_limits": obj(nil)}), nil, nil)
	add("/api/v1/tasks/{id}/policy-blocks", "get", "listPolicyBlocks", "查看任务策略阻断", arr(ref("PolicyBlock")), []any{taskID}, nil)
	add("/api/v1/tasks/{id}/workspace", "get", "listTaskWorkspaceFiles", "列出任务文件", arr(ref("WorkspaceFile")), []any{taskID}, nil)
	path := "/api/v1/tasks/{id}/workspace/{intent}/{name}"
	add(path, "get", "getTaskWorkspaceFile", "预览或下载任务文件", obj(map[string]any{"intent_id": id, "name": str, "size": num, "text_preview": str, "preview_available": map[string]any{"type": "boolean"}}), []any{taskID, map[string]any{"name": "intent", "in": "path", "required": true, "schema": id}, map[string]any{"name": "name", "in": "path", "required": true, "schema": str}}, nil)
	op := paths[path].(map[string]any)["get"].(map[string]any)
	op["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/octet-stream"] = map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}
	add("/api/v1/tasks/{id}/conversation", "get", "listTaskConversation", "查看任务会话", arr(ref("ConversationMessage")), []any{taskID}, nil)
	add("/api/v1/tasks/{id}/conversation", "post", "postTaskConversation", "发送人在环路指令", obj(map[string]any{"reply": str, "message_id": id}), []any{taskID, map[string]any{"name": "X-CSRF-Token", "in": "header", "required": true, "schema": str}}, ref("ConversationWrite"))
	postResponses := paths["/api/v1/tasks/{id}/conversation"].(map[string]any)["post"].(map[string]any)["responses"].(map[string]any)
	postResponses["201"] = postResponses["200"]
	delete(postResponses, "200")
}

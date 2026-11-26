package api

func addPhase4OpenAPI(spec map[string]any) {
	paths := spec["paths"].(map[string]any)
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	str := map[string]any{"type": "string"}
	id := map[string]any{"type": "string", "format": "uuid"}
	boolean := map[string]any{"type": "boolean"}
	integer := map[string]any{"type": "integer"}
	object := func(required []string, properties map[string]any) map[string]any {
		return map[string]any{"type": "object", "required": required, "properties": properties}
	}
	array := func(item any) map[string]any { return map[string]any{"type": "array", "items": item} }
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	date := map[string]any{"type": "string", "format": "date-time"}
	schemas["ToolCall"] = object([]string{"id", "task_id", "intent_id", "run_id", "name", "input", "status", "scope_version", "created_at"}, map[string]any{"id": id, "task_id": id, "intent_id": id, "run_id": id, "name": str, "input": object(nil, nil), "status": str, "policy_reason": str, "summary": str, "output": object(nil, nil), "scope_version": integer, "created_at": date, "completed_at": date})
	schemas["ToolApproval"] = object([]string{"id", "tool_call_id", "task_id", "tool_name", "status", "requested_at"}, map[string]any{"id": id, "tool_call_id": id, "task_id": id, "tool_name": str, "status": str, "reason": str, "requested_at": date, "decided_at": date})
	schemas["Skill"] = object([]string{"id", "manifest", "content_hash", "enabled"}, map[string]any{"id": id, "manifest": object([]string{"name", "version", "description"}, map[string]any{"name": str, "version": str, "description": str, "agents": array(str), "mcps": array(str), "capabilities": array(str)}), "content_hash": str, "enabled": boolean})
	schemas["MCPServer"] = object([]string{"id", "name", "transport", "enabled"}, map[string]any{"id": id, "name": str, "transport": str, "endpoint": str, "command": str, "args": array(str), "enabled": boolean})
	schemas["MCPTool"] = object([]string{"name", "description", "input_schema", "schema_hash", "enabled"}, map[string]any{"name": str, "description": str, "input_schema": object(nil, nil), "schema_hash": str, "enabled": boolean})
	schemas["CustomHTTPTool"] = object([]string{"id", "name", "url_template", "method", "input_schema", "enabled"}, map[string]any{"id": id, "name": str, "description": str, "url_template": str, "method": str, "input_schema": object(nil, nil), "enabled": boolean})
	problem := map[string]any{"description": "错误", "content": map[string]any{"application/problem+json": map[string]any{"schema": ref("Problem")}}}
	response := func(schema any) map[string]any {
		return map[string]any{"description": "成功", "content": map[string]any{"application/json": map[string]any{"schema": object([]string{"data", "meta"}, map[string]any{"data": schema, "meta": object([]string{"request_id"}, map[string]any{"request_id": str})})}}}
	}
	pathParam := func(name string) map[string]any {
		return map[string]any{"name": name, "in": "path", "required": true, "schema": str}
	}
	add := func(path, method, operation, summary string, result, request any, params ...any) {
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
	flag := object([]string{"enabled"}, map[string]any{"enabled": boolean})
	add("/api/v1/tools", "get", "listTools", "列出 Worker 工具绑定", object(nil, nil), nil)
	add("/api/v1/tools/{name}/binding", "put", "setToolBinding", "启停内置工具", flag, flag, pathParam("name"))
	add("/api/v1/tasks/{id}/tool-calls", "get", "listTaskToolCalls", "列出任务工具调用", array(ref("ToolCall")), nil, pathParam("id"))
	add("/api/v1/tool-calls/{id}/output", "get", "downloadToolOutput", "下载完整工具输出", object(nil, nil), nil, pathParam("id"))
	add("/api/v1/approvals", "get", "listApprovals", "列出工具审批", array(ref("ToolApproval")), nil)
	for _, action := range []string{"approve", "reject"} {
		add("/api/v1/approvals/{id}:"+action, "post", action+"ToolCall", "决定工具审批", object(nil, nil), object(nil, map[string]any{"reason": str}), pathParam("id"))
	}
	add("/api/v1/skills", "get", "listSkills", "列出 Skill 摘要", array(ref("Skill")), nil)
	add("/api/v1/skills", "post", "installSkill", "安装 Skill ZIP", ref("Skill"), nil)
	add("/api/v1/skills/{id}", "put", "setSkillEnabled", "启停 Skill", flag, flag, pathParam("id"))
	add("/api/v1/mcp-servers", "get", "listMCPServers", "列出 MCP 服务", array(ref("MCPServer")), nil)
	add("/api/v1/mcp-servers", "post", "createMCPServer", "登记 MCP 服务", ref("MCPServer"), ref("MCPServer"))
	add("/api/v1/mcp-servers/{name}", "put", "setMCPServer", "启停 MCP 服务", flag, flag, pathParam("name"))
	add("/api/v1/mcp-servers/{name}/discover", "post", "discoverMCPTools", "发现 MCP 工具", array(ref("MCPTool")), nil, pathParam("name"))
	add("/api/v1/mcp-tools", "get", "listMCPTools", "列出 MCP 工具", object(nil, nil), nil)
	add("/api/v1/mcp-servers/{name}/tools/{tool}", "put", "setMCPTool", "启停 MCP 工具", flag, flag, pathParam("name"), pathParam("tool"))
	add("/api/v1/custom-tools", "get", "listCustomTools", "列出自定义 HTTP 工具", array(ref("CustomHTTPTool")), nil)
	add("/api/v1/custom-tools", "post", "createCustomTool", "创建只读 HTTP 工具", ref("CustomHTTPTool"), ref("CustomHTTPTool"))
	add("/api/v1/custom-tools/{name}", "put", "setCustomTool", "启停自定义 HTTP 工具", flag, flag, pathParam("name"))
}

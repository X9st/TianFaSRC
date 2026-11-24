package api

func addPhase1OpenAPI(spec map[string]any) {
	paths := spec["paths"].(map[string]any)
	components := spec["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	stringProp := map[string]any{"type": "string"}
	idProp := map[string]any{"type": "string", "format": "uuid"}
	integer := map[string]any{"type": "integer"}
	object := func(required []string, properties map[string]any) map[string]any {
		return map[string]any{"type": "object", "required": required, "properties": properties}
	}
	array := func(item any) map[string]any { return map[string]any{"type": "array", "items": item} }
	schemas["User"] = object([]string{"id", "organization_id", "email", "role", "status"}, map[string]any{"id": idProp, "organization_id": idProp, "email": map[string]any{"type": "string", "format": "email"}, "role": map[string]any{"type": "string", "enum": []string{"admin", "operator", "reviewer", "viewer"}}, "status": stringProp, "created_at": map[string]any{"type": "string", "format": "date-time"}})
	schemas["ScopeEntry"] = object([]string{"effect", "kind", "value", "ports"}, map[string]any{"id": idProp, "effect": map[string]any{"type": "string", "enum": []string{"allow", "deny"}}, "kind": map[string]any{"type": "string", "enum": []string{"domain", "ip", "cidr", "url_prefix", "service"}}, "value": stringProp, "ports": array(integer), "path_prefix": stringProp, "normalized_value": stringProp})
	schemas["Goal"] = object([]string{"text", "priority"}, map[string]any{"id": idProp, "text": stringProp, "priority": integer, "state": stringProp})
	schemas["Constraint"] = object([]string{"effect", "text"}, map[string]any{"id": idProp, "effect": stringProp, "text": stringProp, "source": stringProp})
	schemas["Task"] = object([]string{"id", "organization_id", "title", "status", "version", "scope_version"}, map[string]any{"id": idProp, "organization_id": idProp, "title": stringProp, "description": stringProp, "status": stringProp, "policy_mode": stringProp, "worker_limit": integer, "deadline_at": map[string]any{"type": []string{"string", "null"}, "format": "date-time"}, "budget": map[string]any{"type": "object", "additionalProperties": true}, "version": integer, "scope_version": integer, "created_by": idProp, "created_at": map[string]any{"type": "string", "format": "date-time"}, "updated_at": map[string]any{"type": "string", "format": "date-time"}})
	schemas["TaskDetail"] = object([]string{"task", "goals", "constraints", "scope"}, map[string]any{"task": ref("Task"), "goals": array(ref("Goal")), "constraints": array(ref("Constraint")), "scope": array(ref("ScopeEntry"))})
	schemas["Node"] = object([]string{"id", "kind", "state", "summary"}, map[string]any{"id": idProp, "kind": stringProp, "state": stringProp, "summary": stringProp, "source_intent_id": idProp, "run_id": idProp, "tool_call_id": idProp, "created_at": map[string]any{"type": "string", "format": "date-time"}})
	schemas["Graph"] = object([]string{"nodes", "edges", "anchors"}, map[string]any{"nodes": array(ref("Node")), "edges": array(object([]string{"from_node_id", "to_node_id", "kind"}, map[string]any{"from_node_id": idProp, "to_node_id": idProp, "kind": stringProp})), "anchors": array(object([]string{"node_id", "asset_id", "asset_kind", "asset_value"}, map[string]any{"node_id": idProp, "asset_id": idProp, "asset_kind": stringProp, "asset_value": stringProp}))})
	schemas["Coverage"] = object([]string{"asset_count", "anchored_assets", "goal_count", "proven_goal_count"}, map[string]any{"asset_count": integer, "anchored_assets": integer, "goal_count": integer, "proven_goal_count": integer})
	schemas["Asset"] = object([]string{"id", "organization_id", "kind", "canonical_key", "display_value"}, map[string]any{"id": idProp, "organization_id": idProp, "kind": stringProp, "canonical_key": stringProp, "display_value": stringProp, "raw_value": stringProp, "parent_id": idProp, "source_task_id": idProp, "attributes": map[string]any{"type": "object", "additionalProperties": true}, "confidence": map[string]any{"type": "number"}, "first_seen_at": map[string]any{"type": "string", "format": "date-time"}, "last_seen_at": map[string]any{"type": "string", "format": "date-time"}})
	schemas["Activity"] = object([]string{"id", "event_id", "task_id", "kind", "summary", "occurred_at"}, map[string]any{"id": idProp, "event_id": idProp, "task_id": idProp, "kind": stringProp, "summary": stringProp, "occurred_at": map[string]any{"type": "string", "format": "date-time"}})
	schemas["CreateTaskRequest"] = object([]string{"title", "goals", "scope"}, map[string]any{"title": stringProp, "description": stringProp, "goals": array(ref("Goal")), "scope": object([]string{"entries"}, map[string]any{"entries": array(ref("ScopeEntry"))}), "constraints": array(ref("Constraint")), "execution": object(nil, map[string]any{"policy_mode": stringProp, "worker_limit": integer, "deadline_at": map[string]any{"type": []string{"string", "null"}, "format": "date-time"}, "budget": map[string]any{"type": "object", "additionalProperties": true}}), "llm_profile_chain": array(idProp)})
	components["securitySchemes"] = map[string]any{"SessionCookie": map[string]any{"type": "apiKey", "in": "cookie", "name": sessionCookie}}
	envelope := func(data any) map[string]any {
		return object([]string{"data", "meta"}, map[string]any{"data": data, "meta": object([]string{"request_id"}, map[string]any{"request_id": stringProp})})
	}
	response := func(schema any) map[string]any {
		return map[string]any{"description": "成功", "content": map[string]any{"application/json": map[string]any{"schema": envelope(schema)}}}
	}
	body := func(schema any) map[string]any {
		return map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schema}}}
	}
	problem := map[string]any{"description": "错误", "content": map[string]any{"application/problem+json": map[string]any{"schema": ref("Problem")}}}
	pathID := []any{map[string]any{"name": "id", "in": "path", "required": true, "schema": idProp}}
	pagedParams := []any{map[string]any{"name": "cursor", "in": "query", "schema": idProp}, map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 30}}}
	operation := func(id, summary string, result any, request any, params []any, anonymous bool, created bool) map[string]any {
		status := "200"
		if created {
			status = "201"
		}
		op := map[string]any{"operationId": id, "summary": summary, "responses": map[string]any{status: response(result), "400": problem, "401": problem, "403": problem, "404": problem, "409": problem, "422": problem, "503": problem}}
		if request != nil {
			op["requestBody"] = body(request)
		}
		if params != nil {
			op["parameters"] = params
		}
		if !anonymous {
			op["security"] = []any{map[string]any{"SessionCookie": []any{}}}
		}
		return op
	}
	add := func(path, method string, op map[string]any) {
		if method != "get" && path != "/api/v1/auth/login" && path != "/api/v1/auth/logout" {
			params, _ := op["parameters"].([]any)
			op["parameters"] = append(params, map[string]any{"name": "X-CSRF-Token", "in": "header", "required": true, "schema": stringProp})
		}
		item, ok := paths[path].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[path] = item
		}
		item[method] = op
	}
	add("/api/v1/auth/login", "post", operation("login", "登录并设置 Session 与 CSRF Cookie", ref("User"), object([]string{"email", "password"}, map[string]any{"email": stringProp, "password": stringProp}), nil, true, false))
	add("/api/v1/auth/logout", "post", map[string]any{"operationId": "logout", "summary": "注销会话", "security": []any{map[string]any{"SessionCookie": []any{}}}, "parameters": []any{map[string]any{"name": "X-CSRF-Token", "in": "header", "required": true, "schema": stringProp}}, "responses": map[string]any{"204": map[string]any{"description": "已注销"}, "401": problem, "403": problem}})
	add("/api/v1/me", "get", operation("getMe", "当前用户", ref("User"), nil, nil, false, false))
	add("/api/v1/users", "get", operation("listUsers", "列出组织用户", array(ref("User")), nil, pagedParams, false, false))
	add("/api/v1/users", "post", operation("createUser", "创建组织用户", ref("User"), object([]string{"email", "password", "role"}, map[string]any{"email": stringProp, "password": stringProp, "role": stringProp}), nil, false, true))
	add("/api/v1/users/{id}", "patch", operation("updateUser", "修改角色或停用用户", ref("User"), object(nil, map[string]any{"role": stringProp, "status": stringProp}), pathID, false, false))
	add("/api/v1/tasks", "get", operation("listTasks", "列出任务", array(ref("Task")), nil, pagedParams, false, false))
	add("/api/v1/tasks", "post", operation("createTask", "创建任务草稿", ref("TaskDetail"), ref("CreateTaskRequest"), nil, false, true))
	add("/api/v1/tasks/{id}", "get", operation("getTask", "任务详情", ref("TaskDetail"), nil, pathID, false, false))
	add("/api/v1/tasks/{id}", "patch", operation("updateTask", "修改草稿任务", ref("Task"), object([]string{"version"}, map[string]any{"version": integer, "title": stringProp, "description": stringProp}), pathID, false, false))
	add("/api/v1/tasks/{id}:start", "post", operation("startTask", "检查并启动任务；Phase 1 无模型时返回 409", ref("Task"), object([]string{"version"}, map[string]any{"version": integer}), pathID, false, false))
	delete(paths["/api/v1/tasks/{id}:start"].(map[string]any)["post"].(map[string]any)["responses"].(map[string]any), "200")
	add("/api/v1/tasks/{id}/scope", "put", operation("replaceScope", "替换草稿范围并升级版本", ref("TaskDetail"), object([]string{"version", "entries"}, map[string]any{"version": integer, "entries": array(ref("ScopeEntry"))}), pathID, false, false))
	add("/api/v1/tasks/{id}/scope:validate", "post", operation("validateScope", "规范化并校验范围", array(ref("ScopeEntry")), object([]string{"entries"}, map[string]any{"entries": array(ref("ScopeEntry"))}), pathID, false, false))
	add("/api/v1/tasks/{id}/goals", "post", operation("addGoal", "向草稿增加目标并写入探索图", ref("TaskDetail"), object([]string{"version", "text", "priority"}, map[string]any{"version": integer, "text": stringProp, "priority": integer}), pathID, false, true))
	add("/api/v1/tasks/{id}/constraints", "post", operation("addConstraint", "向草稿增加约束", ref("TaskDetail"), object([]string{"version", "effect", "text"}, map[string]any{"version": integer, "effect": stringProp, "text": stringProp}), pathID, false, true))
	add("/api/v1/tasks/{id}/graph", "get", operation("getGraph", "查询探索血缘图", ref("Graph"), nil, pathID, false, false))
	add("/api/v1/tasks/{id}/coverage", "get", operation("getCoverage", "查询任务图覆盖", ref("Coverage"), nil, pathID, false, false))
	add("/api/v1/tasks/{id}/activities", "get", operation("listActivities", "查询任务 Activity", array(ref("Activity")), nil, append(append([]any{}, pathID...), pagedParams[1]), false, false))
	add("/api/v1/nodes/{id}", "get", operation("getNode", "查询探索节点", ref("Node"), nil, pathID, false, false))
	add("/api/v1/nodes/{id}/assets", "post", operation("anchorAsset", "将探索节点锚定到资产", object([]string{"asset_id"}, map[string]any{"asset_id": idProp}), object([]string{"asset_id"}, map[string]any{"asset_id": idProp}), pathID, false, false))
	add("/api/v1/assets", "get", operation("listAssets", "列出组织资产", array(ref("Asset")), nil, append(append([]any{}, pagedParams...), map[string]any{"name": "kind", "in": "query", "schema": stringProp}), false, false))
	add("/api/v1/assets/graph", "get", operation("getAssetGraph", "查询组织资产图", object([]string{"nodes", "edges"}, map[string]any{"nodes": array(ref("Asset")), "edges": array(object([]string{"from_asset_id", "to_asset_id"}, map[string]any{"from_asset_id": idProp, "to_asset_id": idProp}))}), nil, nil, false, false))
	add("/api/v1/assets", "post", operation("upsertAsset", "规范化并合并资产", ref("Asset"), object([]string{"kind", "value"}, map[string]any{"kind": stringProp, "value": stringProp, "parent_id": idProp, "source_task_id": idProp, "attributes": map[string]any{"type": "object", "additionalProperties": true}, "confidence": map[string]any{"type": "number"}}), nil, false, true))
	add("/api/v1/tasks/{id}/assets", "post", operation("linkAsset", "关联现有资产与任务根节点", object([]string{"asset_id"}, map[string]any{"asset_id": idProp}), object([]string{"asset_id"}, map[string]any{"asset_id": idProp}), pathID, false, false))
}

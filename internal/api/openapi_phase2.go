package api

func addPhase2OpenAPI(spec map[string]any) {
	paths := spec["paths"].(map[string]any)
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	str := map[string]any{"type": "string"}
	id := map[string]any{"type": "string", "format": "uuid"}
	integer := map[string]any{"type": "integer"}
	boolProp := map[string]any{"type": "boolean"}
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	obj := func(required []string, props map[string]any) map[string]any {
		return map[string]any{"type": "object", "required": required, "properties": props}
	}
	arr := func(item any) map[string]any { return map[string]any{"type": "array", "items": item} }
	schemas["CredentialWrite"] = obj(nil, map[string]any{"secret_value": map[string]any{"type": "string", "writeOnly": true}, "remove": boolProp})
	schemas["LLMProfile"] = obj([]string{"id", "name", "format", "base_url", "model", "enabled"}, map[string]any{"id": id, "name": str, "format": map[string]any{"type": "string", "enum": []string{"openai-chat", "openai-responses", "anthropic"}}, "base_url": str, "model": str, "credential_ref": map[string]any{"type": []string{"string", "null"}, "format": "uuid"}, "credential_hint": map[string]any{"type": []string{"string", "null"}}, "priority": integer, "enabled": boolProp, "stream": boolProp, "context_window": integer, "max_output_tokens": integer, "rate_per_second": integer, "rate_per_minute": integer, "health_state": str, "circuit_state": map[string]any{"type": "string", "enum": []string{"closed", "open", "half_open"}}, "cooldown_until": map[string]any{"type": []string{"string", "null"}, "format": "date-time"}})
	schemas["LLMProfileWrite"] = obj([]string{"name", "format", "base_url", "model", "context_window", "max_output_tokens", "rate_per_second", "rate_per_minute"}, map[string]any{"name": str, "format": str, "base_url": str, "model": str, "credential": ref("CredentialWrite"), "priority": integer, "enabled": boolProp, "stream": boolProp, "context_window": integer, "max_output_tokens": integer, "rate_per_second": integer, "rate_per_minute": integer, "thinking_mode": str, "reasoning_effort": str, "session_header_name": str, "retry_policy": map[string]any{"type": "object", "additionalProperties": true}})
	schemas["LLMProfilePatch"] = obj(nil, schemas["LLMProfileWrite"].(map[string]any)["properties"].(map[string]any))
	schemas["LLMChain"] = obj([]string{"profiles"}, map[string]any{"profiles": arr(id)})
	schemas["AgentPrompt"] = obj([]string{"role", "version", "body", "content_hash"}, map[string]any{"role": str, "version": integer, "body": str, "content_hash": str})
	schemas["Intent"] = obj([]string{"id", "goal_id", "objective", "success_criteria", "status"}, map[string]any{"id": id, "goal_id": id, "objective": str, "success_criteria": str, "status": str, "blocking_reason": map[string]any{"type": []string{"string", "null"}}, "stop_reason": map[string]any{"type": []string{"string", "null"}}, "attempt": integer, "priority": integer})
	schemas["AgentRun"] = obj([]string{"id", "task_id", "role", "status", "prompt_version", "prompt_hash"}, map[string]any{"id": id, "task_id": id, "intent_id": id, "role": str, "status": str, "prompt_version": integer, "prompt_hash": str, "error_class": str, "started_at": map[string]any{"type": "string", "format": "date-time"}})
	schemas["AgentRunDetail"] = obj([]string{"run", "messages", "attempts"}, map[string]any{"run": ref("AgentRun"), "messages": arr(obj([]string{"ordinal", "role", "trust_level", "content"}, map[string]any{"ordinal": integer, "role": str, "trust_level": str, "content": str})), "attempts": arr(obj([]string{"profile_id", "ordinal", "status"}, map[string]any{"profile_id": id, "ordinal": integer, "status": str, "error_class": str, "input_tokens": integer, "output_tokens": integer, "estimated": boolProp}))})
	problem := map[string]any{"description": "错误", "content": map[string]any{"application/problem+json": map[string]any{"schema": ref("Problem")}}}
	response := func(schema any) map[string]any {
		return map[string]any{"description": "成功", "content": map[string]any{"application/json": map[string]any{"schema": obj([]string{"data", "meta"}, map[string]any{"data": schema, "meta": obj([]string{"request_id"}, map[string]any{"request_id": str})})}}}
	}
	add := func(path, method, operationID, summary string, result, request any, params []any, created bool) {
		code := "200"
		if created {
			code = "201"
		}
		op := map[string]any{"operationId": operationID, "summary": summary, "security": []any{map[string]any{"SessionCookie": []any{}}}, "responses": map[string]any{code: response(result), "400": problem, "401": problem, "403": problem, "404": problem, "409": problem, "422": problem, "503": problem}}
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
	pathID := []any{map[string]any{"name": "id", "in": "path", "required": true, "schema": id}}
	role := []any{map[string]any{"name": "role", "in": "path", "required": true, "schema": map[string]any{"type": "string", "enum": []string{"decomposer", "planner", "worker"}}}}
	add("/api/v1/llm-profiles", "get", "listLLMProfiles", "列出模型 Profile；不回显密钥", arr(ref("LLMProfile")), nil, nil, false)
	add("/api/v1/llm-profiles", "post", "createLLMProfile", "创建加密凭据和模型 Profile", ref("LLMProfile"), ref("LLMProfileWrite"), nil, true)
	add("/api/v1/llm-profiles/{id}", "get", "getLLMProfile", "读取模型 Profile", ref("LLMProfile"), nil, pathID, false)
	add("/api/v1/llm-profiles/{id}", "patch", "updateLLMProfile", "修改模型 Profile 或轮换密钥", ref("LLMProfile"), ref("LLMProfilePatch"), pathID, false)
	add("/api/v1/llm-profiles/{id}", "delete", "deleteLLMProfile", "删除未被引用的模型 Profile", obj(nil, nil), nil, pathID, false)
	deleteResponses := paths["/api/v1/llm-profiles/{id}"].(map[string]any)["delete"].(map[string]any)["responses"].(map[string]any)
	delete(deleteResponses, "200")
	deleteResponses["204"] = map[string]any{"description": "已删除"}
	add("/api/v1/llm-default-chain", "get", "getLLMDefaultChain", "读取系统默认主备链", ref("LLMChain"), nil, nil, false)
	add("/api/v1/llm-default-chain", "put", "setLLMDefaultChain", "设置系统默认主备链", ref("LLMChain"), ref("LLMChain"), nil, false)
	add("/api/v1/tasks/{id}/llm-chain", "get", "getTaskLLMChain", "读取任务模型链", ref("LLMChain"), nil, pathID, false)
	add("/api/v1/tasks/{id}/llm-chain", "put", "setTaskLLMChain", "设置任务模型链", ref("LLMChain"), ref("LLMChain"), pathID, false)
	add("/api/v1/tasks/{id}/agent-routes/{role}", "get", "getAgentRoute", "读取角色子链", ref("LLMChain"), nil, append(append([]any{}, pathID...), role...), false)
	add("/api/v1/tasks/{id}/agent-routes/{role}", "put", "setAgentRoute", "设置角色子链", ref("LLMChain"), ref("LLMChain"), append(append([]any{}, pathID...), role...), false)
	add("/api/v1/tasks/{id}/intents", "get", "listTaskIntents", "列出任务验证方向", arr(ref("Intent")), nil, pathID, false)
	add("/api/v1/tasks/{id}/agent-runs", "get", "listTaskAgentRuns", "列出任务 Agent Run", arr(ref("AgentRun")), nil, pathID, false)
	add("/api/v1/agent-runs/{id}", "get", "getAgentRun", "读取 Transcript 和模型尝试", ref("AgentRunDetail"), nil, pathID, false)
	add("/api/v1/tasks/{id}:advance", "post", "advanceTask", "继续可恢复的模型规划", ref("TaskDetail"), nil, pathID, false)
	add("/api/v1/agent-prompts/{role}", "get", "getAgentPrompt", "读取激活的角色正文与版本", ref("AgentPrompt"), nil, role, false)
	add("/api/v1/agent-prompts/{role}", "put", "updateAgentPrompt", "创建并激活角色正文新版本", ref("AgentPrompt"), obj([]string{"body"}, map[string]any{"body": str}), role, false)
}

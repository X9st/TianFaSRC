package api

func addPhase5OpenAPI(spec map[string]any) {
	paths := spec["paths"].(map[string]any)
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	str := map[string]any{"type": "string"}
	id := map[string]any{"type": "string", "format": "uuid"}
	integer := map[string]any{"type": "integer"}
	date := map[string]any{"type": "string", "format": "date-time"}
	obj := func(required []string, properties map[string]any) map[string]any {
		return map[string]any{"type": "object", "required": required, "properties": properties}
	}
	arr := func(item any) map[string]any { return map[string]any{"type": "array", "items": item} }
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	schemas["CVSS"] = obj([]string{"version", "vector", "score"}, map[string]any{"version": str, "vector": str, "score": map[string]any{"type": "number"}})
	schemas["Traffic"] = obj([]string{"id", "task_id", "method", "url", "host", "path", "status_code", "remote_ip", "request_bytes", "response_bytes", "requested_at"}, map[string]any{"id": id, "task_id": id, "run_id": id, "intent_id": id, "tool_call_id": id, "method": str, "url": str, "host": str, "path": str, "status_code": integer, "remote_ip": str, "tls": obj(nil, nil), "request_headers": obj(nil, nil), "response_headers": obj(nil, nil), "request_sha256": str, "response_sha256": str, "request_bytes": integer, "response_bytes": integer, "request_body_bytes": integer, "response_body_bytes": integer, "request_body_sha256": str, "response_body_sha256": str, "requested_at": date, "responded_at": date})
	schemas["Finding"] = obj([]string{"id", "task_id", "node_id", "fingerprint", "title", "vulnerability_type", "location", "severity", "status", "asset_ids", "evidence_version", "version"}, map[string]any{"id": id, "task_id": id, "node_id": id, "fingerprint": str, "title": str, "vulnerability_type": str, "location": str, "severity": str, "cvss": ref("CVSS"), "status": str, "asset_ids": arr(id), "preconditions": str, "impact": str, "reproduction_steps": arr(str), "observed_result": str, "recommendation": str, "source_intent_id": id, "evidence_version": integer, "report_evidence_version": integer, "version": integer, "created_at": date, "updated_at": date})
	schemas["Evidence"] = obj([]string{"binding_id", "finding_id", "snapshot_id", "role", "note", "position", "metadata", "captured_at"}, map[string]any{"binding_id": id, "finding_id": id, "snapshot_id": id, "role": str, "note": str, "position": integer, "metadata": obj(nil, nil), "request_headers": obj(nil, nil), "response_headers": obj(nil, nil), "request_sha256": str, "response_sha256": str, "captured_at": date})
	schemas["Retest"] = obj([]string{"id", "finding_id", "task_id", "evidence_version", "scope_version", "status", "created_at"}, map[string]any{"id": id, "finding_id": id, "task_id": id, "evidence_version": integer, "scope_version": integer, "status": str, "conclusion": str, "note": str, "created_at": date, "completed_at": date})
	schemas["Report"] = obj([]string{"task_id", "body", "version", "evidence_version", "current_evidence_version", "stale"}, map[string]any{"task_id": id, "body": str, "version": integer, "evidence_version": integer, "current_evidence_version": integer, "stale": map[string]any{"type": "boolean"}, "updated_at": date})
	schemas["ProxyToken"] = obj([]string{"token", "task_id", "expires_at"}, map[string]any{"token": str, "task_id": id, "expires_at": date})
	problem := map[string]any{"description": "错误", "content": map[string]any{"application/problem+json": map[string]any{"schema": ref("Problem")}}}
	response := func(schema any) map[string]any {
		return map[string]any{"description": "成功", "content": map[string]any{"application/json": map[string]any{"schema": obj([]string{"data", "meta"}, map[string]any{"data": schema, "meta": obj([]string{"request_id"}, map[string]any{"request_id": str})})}}}
	}
	param := func(name, where string) map[string]any {
		return map[string]any{"name": name, "in": where, "required": where == "path", "schema": str}
	}
	add := func(path, method, operation, summary string, result, request any, created bool, params ...any) {
		status := "200"
		if created {
			status = "201"
		}
		op := map[string]any{"operationId": operation, "summary": summary, "security": []any{map[string]any{"SessionCookie": []any{}}}, "responses": map[string]any{status: response(result), "400": problem, "401": problem, "403": problem, "404": problem, "409": problem, "422": problem, "503": problem}}
		if method != "get" {
			params = append(params, param("X-CSRF-Token", "header"))
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
	addRaw := func(path, method, operation, summary, mime string, params ...any) {
		op := map[string]any{"operationId": operation, "summary": summary, "security": []any{map[string]any{"SessionCookie": []any{}}}, "responses": map[string]any{"200": map[string]any{"description": "原始内容", "content": map[string]any{mime: map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}}}, "401": problem, "403": problem, "404": problem, "503": problem}, "parameters": params}
		item, ok := paths[path].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[path] = item
		}
		item[method] = op
	}
	add("/api/v1/traffic", "get", "listTraffic", "查询流量索引", arr(ref("Traffic")), nil, false, param("task_id", "query"), param("host", "query"), param("path", "query"), param("status_code", "query"), param("after", "query"), param("before", "query"), param("body_keyword", "query"), param("min_response_bytes", "query"), param("max_response_bytes", "query"), param("cursor", "query"), param("limit", "query"))
	add("/api/v1/traffic/{id}", "get", "getTraffic", "读取流量元数据", ref("Traffic"), nil, false, param("id", "path"), param("reveal", "query"))
	addRaw("/api/v1/traffic/{id}/body", "get", "getTrafficBody", "校验并读取原始流量", "application/octet-stream", param("id", "path"), param("part", "query"))
	add("/api/v1/findings", "get", "listFindings", "列出漏洞", arr(ref("Finding")), nil, false, param("task_id", "query"))
	add("/api/v1/findings", "post", "createFinding", "创建漏洞草稿", ref("Finding"), obj([]string{"task_id", "title", "vulnerability_type", "location", "severity", "asset_ids"}, map[string]any{"task_id": id, "title": str, "vulnerability_type": str, "location": str, "severity": str, "cvss": ref("CVSS"), "asset_ids": arr(id), "preconditions": str, "impact": str, "reproduction_steps": arr(str), "observed_result": str, "recommendation": str, "source_intent_id": id, "source_run_id": id}), true)
	add("/api/v1/findings/{id}", "get", "getFinding", "读取漏洞", ref("Finding"), nil, false, param("id", "path"))
	add("/api/v1/findings/{id}:confirm", "post", "confirmFinding", "证据复核后确认漏洞", ref("Finding"), obj([]string{"version"}, map[string]any{"version": integer}), false, param("id", "path"))
	add("/api/v1/findings/{id}/evidence", "get", "listEvidence", "列出有序证据", arr(ref("Evidence")), nil, false, param("id", "path"))
	add("/api/v1/findings/{id}/evidence", "post", "bindEvidence", "绑定流量为不可变证据", ref("Evidence"), obj([]string{"traffic_id", "role"}, map[string]any{"traffic_id": id, "role": str, "note": str}), true, param("id", "path"))
	add("/api/v1/evidence-bindings/{id}", "patch", "updateEvidenceBinding", "修改证据用途与说明", ref("Evidence"), obj([]string{"role", "note"}, map[string]any{"role": str, "note": str}), false, param("id", "path"))
	add("/api/v1/findings/{id}/evidence-order", "put", "reorderEvidence", "调整证据顺序", arr(ref("Evidence")), obj([]string{"binding_ids"}, map[string]any{"binding_ids": arr(id)}), false, param("id", "path"))
	addRaw("/api/v1/evidence-bindings/{id}/body", "get", "getEvidenceBody", "校验并下载证据原字节", "application/octet-stream", param("id", "path"), param("part", "query"))
	add("/api/v1/findings/{id}/retests", "get", "listRetests", "列出复测", arr(ref("Retest")), nil, false, param("id", "path"))
	add("/api/v1/findings/{id}/retests", "post", "createRetest", "创建固定证据与范围版本的复测", ref("Retest"), nil, true, param("id", "path"))
	add("/api/v1/retests/{id}", "get", "getRetest", "读取复测", ref("Retest"), nil, false, param("id", "path"))
	add("/api/v1/retests/{id}:complete", "post", "completeRetest", "附验证证据并完成复测", ref("Retest"), obj([]string{"conclusion", "binding_ids"}, map[string]any{"conclusion": str, "binding_ids": arr(id), "note": str}), false, param("id", "path"))
	add("/api/v1/tasks/{id}/report", "get", "getReport", "读取报告及过期状态", ref("Report"), nil, false, param("id", "path"))
	add("/api/v1/tasks/{id}/report", "put", "updateReport", "乐观锁保存报告结论", ref("Report"), obj([]string{"body", "version", "evidence_version"}, map[string]any{"body": str, "version": integer, "evidence_version": integer}), false, param("id", "path"))
	add("/api/v1/tasks/{id}/proxy-token", "post", "issueProxyToken", "生成一小时任务代理令牌", ref("ProxyToken"), nil, true, param("id", "path"))
	addRaw("/api/v1/proxy/ca", "get", "downloadRecordingCA", "下载记录代理 CA 证书", "application/x-pem-file")
	addRaw("/api/v1/tasks/{id}/export", "get", "exportReport", "导出 Markdown、JSON 或证据 ZIP", "application/zip", param("id", "path"), param("format", "query"))
	for _, path := range []string{"/api/v1/traffic/{id}", "/api/v1/evidence-bindings/{id}"} {
		item, ok := paths[path].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[path] = item
		}
		item["delete"] = map[string]any{"operationId": map[string]string{"/api/v1/traffic/{id}": "deleteTraffic", "/api/v1/evidence-bindings/{id}": "unbindEvidence"}[path], "summary": "删除普通流量或解除证据绑定", "security": []any{map[string]any{"SessionCookie": []any{}}}, "parameters": []any{param("id", "path"), param("X-CSRF-Token", "header")}, "responses": map[string]any{"204": map[string]any{"description": "已删除"}, "401": problem, "403": problem, "404": problem, "503": problem}}
	}
}

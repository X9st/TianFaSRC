package api

import "gopkg.in/yaml.v3"

// OpenAPI is generated from the API response contract and route schemas.
// Route changes must update this source and regenerate api/openapi.yaml.
func OpenAPI() ([]byte, error) {
	response := func(description string) map[string]any {
		return map[string]any{
			"description": description,
			"content": map[string]any{"application/json": map[string]any{
				"schema": map[string]any{"$ref": "#/components/schemas/HealthResponse"},
			}},
		}
	}
	spec := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title": "TianFa API", "version": "0.1.0",
			"description": "授权漏洞挖掘平台 API。当前文档覆盖 Phase 0 至 Phase 6 已实现能力。",
		},
		"servers": []any{map[string]any{"url": "/"}},
		"paths": map[string]any{
			LivePath: map[string]any{"get": map[string]any{
				"operationId": "getLiveness", "summary": "进程存活检查",
				"responses": map[string]any{"200": response("进程存活")},
			}},
			ReadyPath: map[string]any{"get": map[string]any{
				"operationId": "getReadiness", "summary": "依赖就绪检查",
				"responses": map[string]any{
					"200": response("所有关键依赖就绪"),
					"503": response("至少一个关键依赖未就绪"),
				},
			}},
		},
		"components": map[string]any{"schemas": map[string]any{
			"HealthCheck": map[string]any{
				"type": "object", "required": []string{"status"},
				"properties": map[string]any{
					"status": map[string]any{"type": "string", "enum": []string{"up", "down"}},
					"detail": map[string]any{"type": "string"},
				},
			},
			"HealthResponse": map[string]any{
				"type": "object", "required": []string{"status", "service", "version", "checks"},
				"properties": map[string]any{
					"status":  map[string]any{"type": "string", "enum": []string{"ok", "not_ready"}},
					"service": map[string]any{"type": "string", "const": "tianfa-server"},
					"version": map[string]any{"type": "string"},
					"checks":  map[string]any{"type": "object", "additionalProperties": map[string]any{"$ref": "#/components/schemas/HealthCheck"}},
				},
			},
			"Problem": map[string]any{
				"type": "object", "required": []string{"type", "title", "status", "request_id"},
				"properties": map[string]any{
					"type":       map[string]any{"type": "string", "format": "uri"},
					"title":      map[string]any{"type": "string"},
					"status":     map[string]any{"type": "integer"},
					"detail":     map[string]any{"type": "string"},
					"instance":   map[string]any{"type": "string"},
					"request_id": map[string]any{"type": "string"},
				},
			},
		}},
	}
	addPhase1OpenAPI(spec)
	addPhase2OpenAPI(spec)
	addPhase3OpenAPI(spec)
	addPhase4OpenAPI(spec)
	addPhase5OpenAPI(spec)
	addPhase6OpenAPI(spec)
	return yaml.Marshal(spec)
}

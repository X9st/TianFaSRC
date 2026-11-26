package tool

import (
	"encoding/json"
	"errors"

	"github.com/google/jsonschema-go/jsonschema"
)

func validateSchema(schemaRaw, inputRaw json.RawMessage) error {
	if len(inputRaw) > 1<<20 {
		return errors.New("工具 Schema 或输入超过上限")
	}
	resolved, err := resolveSchema(schemaRaw)
	if err != nil {
		return err
	}
	var input any
	if err := json.Unmarshal(inputRaw, &input); err != nil {
		return err
	}
	if _, ok := input.(map[string]any); !ok {
		return errors.New("工具输入必须是 JSON 对象")
	}
	return resolved.Validate(input)
}

func resolveSchema(schemaRaw json.RawMessage) (*jsonschema.Resolved, error) {
	if len(schemaRaw) == 0 || len(schemaRaw) > 64<<10 {
		return nil, errors.New("工具 Schema 超过上限")
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		return nil, err
	}
	if schema.Type != "object" {
		return nil, errors.New("工具 Schema 顶层必须是对象")
	}
	closeObjects(&schema)
	return schema.Resolve(nil)
}

func closeObjects(schema *jsonschema.Schema) {
	if schema == nil {
		return
	}
	if schema.Type == "object" && schema.AdditionalProperties == nil {
		schema.AdditionalProperties = &jsonschema.Schema{Not: &jsonschema.Schema{}}
	}
	for _, child := range schema.Properties {
		closeObjects(child)
	}
	for _, child := range schema.Defs {
		closeObjects(child)
	}
	for _, child := range schema.AllOf {
		closeObjects(child)
	}
	for _, child := range schema.AnyOf {
		closeObjects(child)
	}
	for _, child := range schema.OneOf {
		closeObjects(child)
	}
	closeObjects(schema.Items)
}

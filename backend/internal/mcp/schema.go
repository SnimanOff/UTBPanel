package mcp

import (
	"encoding/json"
	"fmt"
	"strings"

	spec "github.com/SnimanOff/UTBPanel/backend/api"
	"github.com/google/jsonschema-go/jsonschema"
	"go.yaml.in/yaml/v3"
)

type parameter struct {
	Name     string         `yaml:"name"`
	In       string         `yaml:"in"`
	Required bool           `yaml:"required"`
	Schema   map[string]any `yaml:"schema"`
}
type operation struct {
	spec.Operation
	Parameters []parameter
	Schema     map[string]any
	Validator  *jsonschema.Resolved
	MediaType  string
}

func operations() ([]operation, error) {
	var doc struct {
		Components struct {
			Schemas    map[string]map[string]any `yaml:"schemas"`
			Parameters map[string]yaml.Node      `yaml:"parameters"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(spec.YAML, &doc); err != nil {
		return nil, err
	}
	ops, err := spec.Operations()
	if err != nil {
		return nil, err
	}
	var out []operation
	for _, op := range ops {
		if op.MCP == spec.Never {
			continue
		}
		if op.MCP != spec.Read && op.MCP != spec.Write && op.MCP != spec.Confirm {
			return nil, fmt.Errorf("%s: missing MCP level", op.ID)
		}
		current := operation{Operation: op}
		properties := map[string]any{}
		var required []string
		positions := map[string]int{}
		for _, node := range op.Parameters {
			var ref struct {
				Ref string `yaml:"$ref"`
			}
			if err := node.Decode(&ref); err != nil {
				return nil, err
			}
			if ref.Ref != "" {
				name, ok := strings.CutPrefix(ref.Ref, "#/components/parameters/")
				if !ok {
					return nil, fmt.Errorf("unsupported parameter reference")
				}
				var exists bool
				node, exists = doc.Components.Parameters[name]
				if !exists {
					return nil, fmt.Errorf("unknown parameter %s", name)
				}
			}
			var p parameter
			if err := node.Decode(&p); err != nil {
				return nil, err
			}
			if p.Name == "" || (p.In != "query" && p.In != "path") {
				return nil, fmt.Errorf("%s: unsupported parameter", op.ID)
			}
			key := p.In + ":" + p.Name
			if pos, ok := positions[key]; ok {
				current.Parameters[pos] = p
			} else {
				positions[key] = len(current.Parameters)
				current.Parameters = append(current.Parameters, p)
			}
		}
		for _, p := range current.Parameters {
			if _, ok := properties[p.Name]; ok {
				return nil, fmt.Errorf("%s: ambiguous input %s", op.ID, p.Name)
			}
			properties[p.Name] = p.Schema
			if p.Required || p.In == "path" {
				required = append(required, p.Name)
			}
		}
		if op.RequestBody.Kind != 0 {
			var body struct {
				Required bool `yaml:"required"`
				Content  map[string]struct {
					Schema map[string]any `yaml:"schema"`
				} `yaml:"content"`
			}
			if err := op.RequestBody.Decode(&body); err != nil {
				return nil, err
			}
			jsonBody, ok := body.Content["application/json"]
			current.MediaType = "application/json"
			if !ok {
				jsonBody, ok = body.Content["multipart/form-data"]
				if !ok {
					return nil, fmt.Errorf("%s: unsupported request body", op.ID)
				}
				current.MediaType = "multipart/form-data"
				if properties, ok := jsonBody.Schema["properties"].(map[string]any); ok {
					for _, value := range properties {
						if field, ok := value.(map[string]any); ok && field["format"] == "binary" {
							delete(field, "format")
							field["contentEncoding"] = "base64"
							field["description"] = "File contents encoded as base64"
						}
					}
				}
			}
			if _, exists := properties["body"]; exists {
				return nil, fmt.Errorf("%s: ambiguous body parameter", op.ID)
			}
			properties["body"] = jsonBody.Schema
			if body.Required {
				required = append(required, "body")
			}
		}
		current.Schema = map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
		if len(required) > 0 {
			current.Schema["required"] = required
		}
		if err := resolveDefinitions(current.Schema, doc.Components.Schemas); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(current.Schema)
		if err != nil {
			return nil, err
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			return nil, fmt.Errorf("%s schema: %w", op.ID, err)
		}
		current.Validator, err = schema.Resolve(nil)
		if err != nil {
			return nil, fmt.Errorf("%s schema: %w", op.ID, err)
		}
		out = append(out, current)
	}
	return out, nil
}

func resolveDefinitions(schema map[string]any, source map[string]map[string]any) error {
	defs := map[string]any{}
	var walk func(any) error
	walk = func(value any) error {
		switch node := value.(type) {
		case map[string]any:
			for key, value := range node {
				if key == "$ref" {
					ref, ok := value.(string)
					if !ok {
						return fmt.Errorf("invalid schema reference")
					}
					name, ok := strings.CutPrefix(ref, "#/components/schemas/")
					if !ok {
						return fmt.Errorf("external schema reference is not supported")
					}
					node[key] = "#/$defs/" + name
					if _, ok := defs[name]; ok {
						continue
					}
					original, ok := source[name]
					if !ok {
						return fmt.Errorf("unknown schema %s", name)
					}
					// Each tool owns its definitions: rewriting one must not alter others.
					raw, err := json.Marshal(original)
					if err != nil {
						return err
					}
					var copy map[string]any
					if err := json.Unmarshal(raw, &copy); err != nil {
						return err
					}
					defs[name] = copy
					if err := walk(copy); err != nil {
						return err
					}
				} else if err := walk(value); err != nil {
					return err
				}
			}
		case []any:
			for _, value := range node {
				if err := walk(value); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(schema); err != nil {
		return err
	}
	if len(defs) > 0 {
		schema["$defs"] = defs
	}
	return nil
}

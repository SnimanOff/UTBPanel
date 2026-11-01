package botconfig

import (
	"fmt"
	"strings"

	spec "github.com/SnimanOff/UTBPanel/backend/api"
	"go.yaml.in/yaml/v3"
)

// Schema derives the document contract and its reachable definitions from the
// same OpenAPI source used for HTTP operations and generated frontend types.
func Schema() (map[string]any, error) {
	var doc struct {
		Components struct {
			Schemas map[string]map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(spec.YAML, &doc); err != nil {
		return nil, err
	}
	defs := map[string]any{}
	var add func(string) error
	var walk func(any) error
	walk = func(v any) error {
		switch x := v.(type) {
		case map[string]any:
			for k, value := range x {
				if k == "$ref" {
					ref, ok := value.(string)
					if ok && strings.HasPrefix(ref, "#/components/schemas/") {
						name := strings.TrimPrefix(ref, "#/components/schemas/")
						x[k] = "#/$defs/" + name
						if err := add(name); err != nil {
							return err
						}
					}
				} else if err := walk(value); err != nil {
					return err
				}
			}
		case []any:
			for _, value := range x {
				if err := walk(value); err != nil {
					return err
				}
			}
		}
		return nil
	}
	add = func(name string) error {
		if _, ok := defs[name]; ok {
			return nil
		}
		value, ok := doc.Components.Schemas[name]
		if !ok {
			return fmt.Errorf("missing settings schema %s", name)
		}
		defs[name] = value
		return walk(value)
	}
	if err := add("BotConfigDocument"); err != nil {
		return nil, err
	}
	return map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "$ref": "#/$defs/BotConfigDocument", "$defs": defs}, nil
}

// Package spec embeds openapi.yaml: the contract between the frontend and
// the backend, the source of MCP tools and of the docs for neural networks.
package spec

//go:generate go run ./gen

import (
	_ "embed"
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed openapi.yaml
var YAML []byte

// MCP levels of operations.
const (
	Read    = "read"
	Write   = "write"
	Confirm = "confirm"
	Never   = "never"
)

// Operation is one method on one path.
type Operation struct {
	Method      string
	Path        string
	ID          string `yaml:"operationId"`
	Summary     string `yaml:"summary"`
	MCP         string `yaml:"x-mcp"`
	Tags        []string
	Parameters  []yaml.Node `yaml:"parameters"`
	RequestBody yaml.Node   `yaml:"requestBody"`
}

// Pattern is the route as registered in the Go mux: "GET /api/bots/{id}".
func (o Operation) Pattern() string { return o.Method + " " + o.Path }

var methods = []string{"get", "post", "put", "patch", "delete"}

// Operations parses the spec.
func Operations() ([]Operation, error) {
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(YAML, &doc); err != nil {
		return nil, err
	}
	var out []Operation
	for path, item := range doc.Paths {
		for _, m := range methods {
			node, ok := item[m]
			if !ok {
				continue
			}
			var op Operation
			if err := node.Decode(&op); err != nil {
				return nil, fmt.Errorf("%s %s: %w", m, path, err)
			}
			op.Method, op.Path = strings.ToUpper(m), path
			out = append(out, op)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pattern() < out[j].Pattern() })
	return out, nil
}

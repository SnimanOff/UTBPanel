package mcp

import (
	spec "github.com/SnimanOff/UTBPanel/backend/api"
)

// Tool is one MCP tool as the docs for neural networks describe it.
type Tool struct {
	Name    string
	Level   string // read | write | confirm
	Tag     string
	Summary string
	// Input is the JSON Schema of the arguments ($defs inside).
	Input map[string]any
	// Untrusted: the answer may carry {"untrusted": …} values.
	Untrusted bool
}

// Tools lists the tools the MCP server offers, in the order of the spec.
func Tools() ([]Tool, error) {
	ops, err := operations()
	if err != nil {
		return nil, err
	}
	out := []Tool{}
	for _, op := range ops {
		tag := ""
		if len(op.Tags) > 0 {
			tag = op.Tags[0]
		}
		out = append(out, Tool{Name: op.ID, Level: op.MCP, Tag: tag, Summary: op.Summary, Input: op.Schema, Untrusted: op.Untrusted})
	}
	w := waitTool()
	out = append(out, Tool{Name: w.Name, Level: spec.Read, Tag: "mcp", Summary: w.Description, Input: w.InputSchema.(map[string]any)})
	return out, nil
}

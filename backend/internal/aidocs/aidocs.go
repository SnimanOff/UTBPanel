// Package aidocs serves /llms.txt and /docs/ai.md: the panel, its concepts,
// the MCP tools with their levels and the settings document schema for
// neural networks. Everything comes from openapi.yaml, so the docs follow
// the API without anyone remembering to update them.
package aidocs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	spec "github.com/SnimanOff/UTBPanel/backend/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/botconfig"
	"github.com/SnimanOff/UTBPanel/backend/internal/mcp"
	"go.yaml.in/yaml/v3"
)

// Endpoint is where MCP clients connect (through gate).
const Endpoint = "http://127.0.0.1:47100/mcp"

type document struct {
	Info struct {
		Title       string `yaml:"title"`
		Description string `yaml:"description"`
	} `yaml:"info"`
	Tags []struct {
		Name string `yaml:"name"`
	} `yaml:"tags"`
	Components struct {
		Schemas map[string]map[string]any `yaml:"schemas"`
	} `yaml:"components"`
}

var levelText = map[string]string{
	spec.Read:    "read",
	spec.Write:   "write",
	spec.Confirm: "confirm — waits for the owner",
}

// inputs renders tool arguments: "id*: integer, limit: integer, body*: {name*, hint}".
func inputs(schema map[string]any) string {
	defs, _ := schema["$defs"].(map[string]any)
	resolve := func(s map[string]any) map[string]any {
		for i := 0; s != nil && i < 4; i++ {
			ref, _ := s["$ref"].(string)
			name, ok := strings.CutPrefix(ref, "#/$defs/")
			if !ok {
				break
			}
			s, _ = defs[name].(map[string]any)
		}
		return s
	}
	required := func(s map[string]any) map[string]bool {
		out := map[string]bool{}
		switch r := s["required"].(type) {
		case []any:
			for _, v := range r {
				out[fmt.Sprint(v)] = true
			}
		case []string:
			for _, v := range r {
				out[v] = true
			}
		}
		return out
	}
	typeOf := func(s map[string]any) string {
		s = resolve(s)
		if s == nil {
			return "any"
		}
		if e, ok := s["enum"].([]any); ok {
			parts := []string{}
			for _, v := range e {
				parts = append(parts, fmt.Sprint(v))
			}
			return strings.Join(parts, "|")
		}
		if t, ok := s["type"].(string); ok {
			return t
		}
		return "any"
	}
	var parts []string
	props, _ := schema["properties"].(map[string]any)
	req := required(schema)
	names := []string{}
	for name := range props {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		// Path and query first, the body last.
		if (names[i] == "body") != (names[j] == "body") {
			return names[j] == "body"
		}
		return names[i] < names[j]
	})
	for _, name := range names {
		s, _ := props[name].(map[string]any)
		mark := ""
		if req[name] {
			mark = "*"
		}
		if name == "body" {
			body := resolve(s)
			if inner, ok := body["properties"].(map[string]any); ok && len(inner) > 0 {
				breq := required(body)
				fields := []string{}
				for f := range inner {
					fm := ""
					if breq[f] {
						fm = "*"
					}
					fields = append(fields, f+fm)
				}
				sort.Strings(fields)
				parts = append(parts, "body"+mark+": {"+strings.Join(fields, ", ")+"}")
				continue
			}
		}
		parts = append(parts, name+mark+": "+typeOf(s))
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, ", ")
}

// fieldLevels lists document fields with the level an AI applies them at.
func fieldLevels(schemas map[string]map[string]any) (write, confirm []string) {
	var walk func(prefix string, s map[string]any, depth int)
	walk = func(prefix string, s map[string]any, depth int) {
		props, _ := s["properties"].(map[string]any)
		for name, raw := range props {
			p, _ := raw.(map[string]any)
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			switch p["x-mcp"] {
			case spec.Write:
				write = append(write, path)
				continue
			case spec.Confirm:
				confirm = append(confirm, path)
				continue
			}
			if depth < 2 {
				walk(path, p, depth+1)
			}
		}
	}
	walk("", schemas["BotConfigDocument"], 0)
	sort.Strings(write)
	sort.Strings(confirm)
	return write, confirm
}

// Build renders llms.txt and ai.md.
func Build() (llms, md string, err error) {
	var doc document
	if err := yaml.Unmarshal(spec.YAML, &doc); err != nil {
		return "", "", err
	}
	tools, err := mcp.Tools()
	if err != nil {
		return "", "", err
	}
	schema, err := botconfig.Schema()
	if err != nil {
		return "", "", err
	}
	order := map[string]int{"mcp": -1}
	for i, t := range doc.Tags {
		order[t.Name] = i
	}
	rank := func(tag string) int {
		if n, ok := order[tag]; ok {
			return n
		}
		return len(doc.Tags) // untagged groups go last
	}
	sort.SliceStable(tools, func(i, j int) bool { return rank(tools[i].Tag) < rank(tools[j].Tag) })
	title := strings.TrimSuffix(doc.Info.Title, " API")
	summary := "Self-hosted panel for Telegram bots written in Go: deploys with rollback, logs, metrics, users, bot databases and settings. Neural networks work with it over MCP with levels and owner confirmations."

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n> %s\n\n", title, summary)
	fmt.Fprintf(&b, "MCP: Streamable HTTP at %s through gate, header `Authorization: Bearer <token>` (the token is in the gate tray menu and is bound to the device key).\n\n", Endpoint)
	b.WriteString("## Docs\n\n- [AI guide](/docs/ai.md): concepts, every MCP tool with its level and arguments, the settings document schema\n- [README](https://github.com/SnimanOff/UTBPanel#readme): installation and how to write a bot for the panel\n\n")
	b.WriteString("## Tools\n\n")
	for _, t := range tools {
		fmt.Fprintf(&b, "- %s (%s): %s\n", t.Name, t.Level, firstSentence(t.Summary))
	}
	llms = b.String()

	b.Reset()
	fmt.Fprintf(&b, "# %s — guide for neural networks\n\n> %s\n\n", title, summary)
	fmt.Fprintf(&b, "Connect over MCP (Streamable HTTP): `%s` with `Authorization: Bearer <token>`.\n", Endpoint)
	fmt.Fprintf(&b, "For Claude Code: `claude mcp add --transport http tgpanel %s --header \"Authorization: Bearer <token>\"`.\n\n", Endpoint)
	b.WriteString(strings.TrimSpace(doc.Info.Description))
	b.WriteString("\n\n## Tools\n\n`*` marks required arguments. ⚠ marks answers that may carry `{\"untrusted\": …}` values.\n")
	tag := "\x00"
	for _, t := range tools {
		if t.Tag != tag {
			tag = t.Tag
			fmt.Fprintf(&b, "\n### %s\n\n| tool | level | arguments | what it does |\n|---|---|---|---|\n", tag)
		}
		warn := ""
		if t.Untrusted {
			warn = " ⚠"
		}
		fmt.Fprintf(&b, "| `%s`%s | %s | %s | %s |\n", t.Name, warn, levelText[t.Level], cell(inputs(t.Input)), cell(t.Summary))
	}
	write, confirm := fieldLevels(doc.Components.Schemas)
	b.WriteString("\n## Settings document\n\n")
	fmt.Fprintf(&b, "Applied at once: %s.\n\nNeed the owner's confirmation: %s.\n\n", codeList(write), codeList(confirm))
	b.WriteString("JSON Schema (also `get_bot_config_schema`):\n\n```json\n")
	raw, _ := json.MarshalIndent(schema, "", "  ")
	b.Write(raw)
	b.WriteString("\n```\n")
	return llms, b.String(), nil
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i]
	}
	return strings.TrimSuffix(s, ".")
}

func cell(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ").Replace(s)
}

func codeList(items []string) string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = "`" + s + "`"
	}
	return strings.Join(out, ", ")
}

var (
	once     sync.Once
	llms, md string
	buildErr error
)

// Register serves the docs; they are built once, from the embedded spec.
func Register(mux *api.Mux) {
	serve := func(kind string, pick func() string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			once.Do(func() { llms, md, buildErr = Build() })
			if buildErr != nil {
				api.WriteError(w, 500, "internal", buildErr.Error())
				return
			}
			w.Header().Set("Content-Type", kind+"; charset=utf-8")
			w.Write([]byte(pick()))
		}
	}
	mux.HandleFunc("GET /llms.txt", serve("text/plain", func() string { return llms }))
	mux.HandleFunc("GET /docs/ai.md", serve("text/markdown", func() string { return md }))
}

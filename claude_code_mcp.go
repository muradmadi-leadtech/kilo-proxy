package main

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/tailscale/hujson"
)

// Claude Code keeps MCP servers in its config directory's .claude.json, beside a
// large amount of unrelated user state. Kilo reads, patches only /mcpServers and
// writes back, so every other key and the file's formatting are preserved.
// Mode B writes "Bearer ${KILO_MCP_..._TOKEN}"; Claude Code expands it from the
// launch environment, and Kilo never stores the secret.
func claudeMCPServerValue(c mcpServerConfig) map[string]any {
	value := map[string]any{"type": "http", "url": c.URL}
	if c.Header == "" {
		return value
	}
	header := c.ValuePrefix
	if c.modeB() {
		header += "${" + c.EnvVar + "}"
	} else {
		header += c.Token
	}
	value["headers"] = map[string]any{c.Header: header}
	return value
}

// managedClaudeMCPServer recognises only the exact shape Kilo writes: a remote
// HTTP server at Kilo's URL carrying no keys but type, url and its own header.
func managedClaudeMCPServer(value hujson.Value, c mcpServerConfig) bool {
	value = value.Clone()
	value.Standardize()
	var server map[string]any
	if json.Unmarshal(value.Pack(), &server) != nil || server["type"] != "http" {
		return false
	}
	for name := range server {
		switch name {
		case "type", "url", "headers":
		default:
			return false
		}
	}
	if address, ok := server["url"].(string); !ok || address != c.URL {
		return false
	}
	headers, ok := server["headers"].(map[string]any)
	if c.Header == "" {
		return !ok
	}
	if !ok || len(headers) == 0 {
		return false
	}
	for name, raw := range headers {
		if !strings.EqualFold(name, c.Header) {
			return false
		}
		if text, ok := raw.(string); !ok || text == "" {
			return false
		}
	}
	return true
}

// mergeClaudeCodeMCP adds, refreshes or removes Kilo-managed curated servers in
// a Claude Code .claude.json. Every unrelated key stays byte-for-byte. A foreign
// entry that collides with a managed name is skipped and reported.
func mergeClaudeCodeMCP(data []byte, servers []mcpServerConfig) ([]byte, []string, error) {
	tree, err := hujson.Parse(data)
	if err != nil || tree.Value.Kind() != '{' || !uniqueEditorJSON(tree) {
		return nil, nil, errors.New("Los ajustes de Claude Code deben ser JSON válido con claves únicas; no se guardó nada.")
	}
	container := tree.Find("/mcpServers")
	if container != nil && container.Value.Kind() != '{' {
		return nil, nil, errors.New("mcpServers de Claude Code debe ser un objeto; no se guardó nada.")
	}
	var operations []map[string]any
	var skipped []string
	createdContainer := false
	for _, c := range servers {
		current := tree.Find("/mcpServers/" + c.ID)
		if current != nil {
			if !managedClaudeMCPServer(*current, c) {
				if c.Enabled {
					skipped = append(skipped, c.ID)
				}
				continue
			}
			if !c.Enabled {
				operations = append(operations, map[string]any{"op": "remove", "path": "/mcpServers/" + c.ID})
				continue
			}
			operations = append(operations, map[string]any{"op": "add", "path": "/mcpServers/" + c.ID, "value": claudeMCPServerValue(c)})
			continue
		}
		if !c.Enabled {
			continue
		}
		if container == nil && !createdContainer {
			operations = append(operations, map[string]any{"op": "add", "path": "/mcpServers", "value": map[string]any{}})
			createdContainer = true
		}
		operations = append(operations, map[string]any{"op": "add", "path": "/mcpServers/" + c.ID, "value": claudeMCPServerValue(c)})
	}
	if len(operations) == 0 {
		return data, skipped, nil
	}
	patch, _ := json.Marshal(operations)
	if err := tree.Patch(patch); err != nil {
		return nil, nil, errors.New("No se pueden actualizar los ajustes MCP de Claude Code; no se guardó nada.")
	}
	result := tree.Pack()
	if len(result) > catalogLimit {
		return nil, nil, errors.New("Los ajustes de Claude Code superan el límite de tamaño.")
	}
	return result, skipped, nil
}

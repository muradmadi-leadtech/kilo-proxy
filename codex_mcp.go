package main

import (
	"errors"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// One curated server as the fan-out wants it in a harness config. The credential
// is already resolved for the chosen delivery mode: Token for mode A (a literal
// secret written into the file) or EnvVar for mode B (a name Kilo sets when it
// launches the harness, so no secret is stored).
type mcpServerConfig struct {
	ID          string
	Enabled     bool
	URL         string
	Header      string
	ValuePrefix string
	Token       string
	EnvVar      string
}

func (c mcpServerConfig) modeB() bool { return c.EnvVar != "" }

// A same-name entry is Kilo's only when it carries exactly the shape Kilo
// writes. Anything else — a different URL, another header, a local command — is
// a foreign entry: never replaced, only reported.
func managedCodexMCPServer(raw any, c mcpServerConfig) bool {
	table, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	for _, field := range []string{"command", "args", "env"} {
		if _, exists := table[field]; exists {
			return false
		}
	}
	if value, ok := table["url"].(string); !ok || value != c.URL {
		return false
	}
	if value, exists := table["bearer_token_env_var"]; exists {
		name, ok := value.(string)
		if !ok || name != c.EnvVar {
			return false
		}
	}
	for _, key := range []string{"http_headers", "env_http_headers"} {
		value, exists := table[key]
		if !exists {
			continue
		}
		headers, ok := value.(map[string]any)
		if !ok || len(headers) == 0 {
			return false
		}
		for name := range headers {
			if !strings.EqualFold(name, c.Header) {
				return false
			}
		}
	}
	return true
}

func codexMCPServerTable(c mcpServerConfig) (map[string]any, error) {
	table := map[string]any{"url": c.URL}
	switch {
	case !c.modeB():
		table["http_headers"] = map[string]any{c.Header: c.ValuePrefix + c.Token}
	case strings.EqualFold(c.Header, "authorization") && c.ValuePrefix == "Bearer ":
		// The launch variable holds the bare token, matching the header Kilo
		// writes for Claude Code: "Bearer ${KILO_MCP_..._TOKEN}".
		table["bearer_token_env_var"] = c.EnvVar
	case c.ValuePrefix == "":
		table["env_http_headers"] = map[string]any{c.Header: c.EnvVar}
	default:
		return nil, errors.New("Este encabezado no se puede entregar por variable de arranque en Codex; usa entrega literal para este servidor")
	}
	return table, nil
}

// mergeCodexMCP adds, refreshes or removes Kilo-managed curated servers in a
// Codex config.toml. Comments, unrelated keys and foreign servers are preserved.
// A foreign entry that collides with a managed name is skipped and reported.
func mergeCodexMCP(data []byte, servers []mcpServerConfig) ([]byte, []string, error) {
	var before, after map[string]any
	if toml.Unmarshal(data, &before) != nil || toml.Unmarshal(data, &after) != nil {
		return nil, nil, errors.New("El TOML de Codex no es válido; no se guardaron los ajustes MCP")
	}
	if after == nil {
		after = map[string]any{}
	}
	var table map[string]any
	if raw, exists := after["mcp_servers"]; exists {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, nil, errors.New("mcp_servers de Codex debe ser una tabla TOML; no se guardaron los ajustes MCP")
		}
		table = m
	}
	var skipped []string
	for _, c := range servers {
		var existing map[string]any
		if table != nil {
			if raw, exists := table[c.ID]; exists {
				if !managedCodexMCPServer(raw, c) {
					if c.Enabled {
						skipped = append(skipped, c.ID)
					}
					continue
				}
				existing, _ = raw.(map[string]any)
			}
		}
		if !c.Enabled {
			if existing != nil {
				delete(table, c.ID)
			}
			continue
		}
		wanted, err := codexMCPServerTable(c)
		if err != nil {
			return nil, nil, err
		}
		if existing == nil {
			existing = map[string]any{}
			if table == nil {
				table = map[string]any{}
				after["mcp_servers"] = table
			}
			table[c.ID] = existing
		}
		// Refresh only the connection fields Kilo owns; keep anything else the
		// user added to this entry (timeouts, tool restrictions, enabled).
		for key := range existing {
			switch key {
			case "url", "http_headers", "env_http_headers", "bearer_token_env_var":
				delete(existing, key)
			}
		}
		for key, value := range wanted {
			existing[key] = value
		}
	}
	out, err := editCodexTOML(data, before, after)
	if err != nil {
		return nil, nil, err
	}
	return out, skipped, nil
}

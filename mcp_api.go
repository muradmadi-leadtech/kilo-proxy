package main

import (
	"net/http"
	"strings"
)

func catalogServerByID(catalog []curatedMCP, id string) (curatedMCP, bool) {
	for _, s := range catalog {
		if s.ID == id {
			return s, true
		}
	}
	return curatedMCP{}, false
}

// mcpAPI is the MCP tab backend. Every route is reachable only through the
// local admin token, like the other /api endpoints.
func (a *app) mcpAPI(w http.ResponseWriter, r *http.Request) {
	switch strings.TrimPrefix(r.URL.Path, "/api/mcp/") {
	case "catalog":
		if r.Method != http.MethodGet {
			jsonError(w, 405, "Method not allowed.")
			return
		}
		a.mcpCatalogView(w, r)
	case "state":
		if r.Method != http.MethodPost {
			jsonError(w, 405, "Method not allowed.")
			return
		}
		a.mcpSetState(w, r)
	case "auth":
		switch r.Method {
		case http.MethodPost:
			a.mcpSetToken(w, r)
		case http.MethodDelete:
			a.mcpClearToken(w, r)
		default:
			jsonError(w, 405, "Method not allowed.")
		}
	case "sync":
		if r.Method != http.MethodPost {
			jsonError(w, 405, "Method not allowed.")
			return
		}
		a.mcpSyncHandler(w, r)
	default:
		jsonError(w, 404, "Unknown MCP endpoint.")
	}
}

func (a *app) mcpCatalogView(w http.ResponseWriter, r *http.Request) {
	catalog := a.mcpCatalog(r.Context())
	store := newMCPStateStore(a.dir)
	servers := make([]map[string]any, 0, len(catalog))
	for _, s := range catalog {
		state := store.value.Servers[s.ID]
		servers = append(servers, map[string]any{
			"id": s.ID, "name": s.Name, "description": s.Description, "url": s.URL,
			"auth": s.Auth, "modes": s.Modes, "envVar": s.EnvVar,
			"docsUrl": s.DocsURL, "tokenUrl": s.TokenURL, "scopes": s.Scopes,
			"enabled": state.Enabled, "mode": state.Mode, "harnesses": state.Harnesses,
			"hasToken": s.Auth == "token" && a.mcpHasToken(s.ID),
		})
	}
	jsonResponse(w, 200, map[string]any{"servers": servers, "harnesses": mcpHarnesses, "warning": store.warning})
}

func (a *app) mcpSetState(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID        string   `json:"id"`
		Enabled   *bool    `json:"enabled"`
		Mode      *string  `json:"mode"`
		Harnesses []string `json:"harnesses"`
	}
	if !decodeXcodeBody(w, r, &body) {
		return
	}
	catalog := a.mcpCatalog(r.Context())
	server, ok := catalogServerByID(catalog, body.ID)
	if !ok {
		jsonError(w, 400, "Unknown MCP server.")
		return
	}
	store := newMCPStateStore(a.dir)
	file := cloneMCPStateFile(store.value)
	state := file.Servers[body.ID]
	if body.Enabled != nil {
		state.Enabled = *body.Enabled
	}
	if body.Mode != nil {
		if *body.Mode != "" && !mcpModes[*body.Mode] {
			jsonError(w, 400, "Unknown delivery mode.")
			return
		}
		if *body.Mode == "B" && server.EnvVar == "" {
			jsonError(w, 400, "This server has no launch variable; use literal delivery.")
			return
		}
		state.Mode = *body.Mode
	}
	if body.Harnesses != nil {
		state.Harnesses = body.Harnesses
	}
	file.Servers[body.ID] = state
	if err := store.save(file, false); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	jsonResponse(w, 200, map[string]any{"ok": true, "server": state})
}

func (a *app) mcpSetToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if !decodeXcodeBody(w, r, &body) {
		return
	}
	server, ok := catalogServerByID(a.mcpCatalog(r.Context()), body.ID)
	if !ok || server.Auth != "token" {
		jsonError(w, 400, "Unknown token server.")
		return
	}
	if !validMCPToken(body.Token) {
		jsonError(w, 400, "Enter a valid token.")
		return
	}
	if err := validateMCPToken(r.Context(), a.transport, server, body.Token); err != nil {
		jsonError(w, 409, "The server did not accept the token: "+err.Error())
		return
	}
	if err := a.vault.Set(mcpVaultSlot(server.ID), body.Token); err != nil {
		jsonError(w, 500, "Cannot store the token in the credential vault.")
		return
	}
	jsonResponse(w, 200, map[string]any{"ok": true})
}

func (a *app) mcpClearToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !decodeXcodeBody(w, r, &body) {
		return
	}
	if _, ok := catalogServerByID(a.mcpCatalog(r.Context()), body.ID); !ok {
		jsonError(w, 400, "Unknown MCP server.")
		return
	}
	if err := a.vault.Delete(mcpVaultSlot(body.ID)); err != nil {
		jsonError(w, 500, "Cannot remove the stored token.")
		return
	}
	jsonResponse(w, 200, map[string]any{"ok": true})
}

func (a *app) mcpSyncHandler(w http.ResponseWriter, r *http.Request) {
	reports, err := a.syncMCP(r.Context())
	if err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	jsonResponse(w, 200, map[string]any{"ok": true, "results": reports})
}

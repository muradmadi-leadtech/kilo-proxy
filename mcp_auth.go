package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	mcpProbeLimit   = 1 << 20
	mcpProbeTimeout = 15 * time.Second
	mcpTokenMax     = 8192
)

// Vault slot for a server token. The headless vault allows only
// [A-Za-z0-9_-], so the name uses hyphens rather than "mcp/<id>/token".
func mcpVaultSlot(id string) string { return "mcp-" + id + "-token" }

func (a *app) mcpHasToken(id string) bool {
	token, err := a.vault.Get(mcpVaultSlot(id))
	return err == nil && token != ""
}

// mcpProbePost sends one JSON-RPC request and returns its result. An auth
// rejection is reported as such; any other non-200 is a transport failure.
func mcpProbePost(ctx context.Context, transport http.RoundTripper, target, header, value string, message map[string]any) (json.RawMessage, error) {
	body, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if header != "" {
		req.Header.Set(header, value)
	}
	client := &http.Client{Transport: transport, Timeout: mcpProbeTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("no se pudo contactar con el servidor")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, errors.New("el servidor rechazó la credencial")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("el servidor respondió HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, mcpProbeLimit+1))
	if err != nil || len(data) > mcpProbeLimit {
		return nil, errors.New("el servidor respondió con un cuerpo demasiado grande")
	}
	var reply struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &reply) != nil {
		return nil, errors.New("el servidor respondió con JSON no válido")
	}
	if reply.Error != nil {
		return nil, fmt.Errorf("el servidor devolvió un error: %s", reply.Error.Message)
	}
	return reply.Result, nil
}

// validateMCPToken proves a pasted credential works before Kilo stores it:
// initialize then tools/list, the same handshake the doc requires.
func validateMCPToken(ctx context.Context, transport http.RoundTripper, server curatedMCP, token string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*mcpProbeTimeout)
	defer cancel()
	value := server.ValuePrefix + token
	initialize := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "kilo-proxy", "version": version},
		},
	}
	if _, err := mcpProbePost(ctx, transport, server.URL, server.Header, value, initialize); err != nil {
		return err
	}
	list := map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}}
	if _, err := mcpProbePost(ctx, transport, server.URL, server.Header, value, list); err != nil {
		return err
	}
	return nil
}

func validMCPToken(token string) bool {
	return strings.TrimSpace(token) != "" && len(token) <= mcpTokenMax && !strings.ContainsAny(token, "\x00\r\n")
}

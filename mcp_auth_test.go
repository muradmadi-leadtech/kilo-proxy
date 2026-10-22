package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type mcpTestVault map[string]string

func (v mcpTestVault) Get(slot string) (string, error) {
	if value, ok := v[slot]; ok {
		return value, nil
	}
	return "", errors.New("not found")
}
func (v mcpTestVault) Set(slot, value string) error { v[slot] = value; return nil }
func (v mcpTestVault) Delete(slot string) error     { delete(v, slot); return nil }

func mcpProbeServer(t *testing.T, wantAuth string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != wantAuth {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		var message struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		if json.Unmarshal(body, &message) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		result := map[string]any{}
		if message.Method == "initialize" {
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "probe", "version": "1"}}
		} else if message.Method == "tools/list" {
			result = map[string]any{"tools": []any{}}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result})
	}))
}

func TestValidateMCPToken(t *testing.T) {
	server := mcpProbeServer(t, "Bearer good-token", http.StatusOK)
	defer server.Close()
	curated := curatedMCP{ID: "x", URL: server.URL, Transport: "http", Auth: "token", Header: "Authorization", ValuePrefix: "Bearer "}
	if err := validateMCPToken(context.Background(), server.Client().Transport, curated, "good-token"); err != nil {
		t.Fatalf("a working token must validate: %v", err)
	}
	if err := validateMCPToken(context.Background(), server.Client().Transport, curated, "bad-token"); err == nil {
		t.Fatal("a rejected token must not validate")
	}
}

func TestMCPVaultSlotIsAddressable(t *testing.T) {
	if !headlessVaultSlot.MatchString(mcpVaultSlot("supermetrics")) {
		t.Fatalf("slot %q must satisfy the headless vault pattern", mcpVaultSlot("supermetrics"))
	}
}

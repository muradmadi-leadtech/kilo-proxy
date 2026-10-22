package main

import (
	"bytes"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

const (
	supermetricsURL = "https://mcp.supermetrics.com/mcp"
	appsflyerURL    = "https://mcp.appsflyer.com/auth/mcp"
)

func mustMCPTOML(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := toml.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func codexServer(t *testing.T, data []byte, id string) map[string]any {
	t.Helper()
	servers, _ := mustMCPTOML(t, data)["mcp_servers"].(map[string]any)
	table, _ := servers[id].(map[string]any)
	return table
}

func TestCodexMCPAddKeepsUnrelatedAndIsIdempotent(t *testing.T) {
	original := []byte("# keep my comment\nmodel = 'vendor/code'\n\n[mcp_servers.docs]\ncommand = 'my-docs'\nargs = ['--local']\n")
	servers := []mcpServerConfig{{ID: "supermetrics", Enabled: true, URL: supermetricsURL, Header: "Authorization", ValuePrefix: "Bearer ", Token: "api_abc"}}
	data, skipped, err := mergeCodexMCP(original, servers)
	if err != nil || len(skipped) != 0 {
		t.Fatalf("merge failed: %v (skipped %v)", err, skipped)
	}
	table := codexServer(t, data, "supermetrics")
	if table["url"] != supermetricsURL {
		t.Fatalf("bad url: %v", table)
	}
	headers, _ := table["http_headers"].(map[string]any)
	if headers["Authorization"] != "Bearer api_abc" {
		t.Fatalf("mode A header missing: %v", table)
	}
	if !bytes.Contains(data, []byte("# keep my comment")) || codexServer(t, data, "docs")["command"] != "my-docs" {
		t.Fatal("unrelated settings changed")
	}
	if _, exists := mustMCPTOML(t, data)["model"]; !exists {
		t.Fatal("unrelated top-level key dropped")
	}
	again, _, err := mergeCodexMCP(data, servers)
	if err != nil || !bytes.Equal(data, again) {
		t.Fatalf("not idempotent: %v", err)
	}
}

func TestCodexMCPModeBStoresNoSecret(t *testing.T) {
	servers := []mcpServerConfig{{ID: "appsflyer", Enabled: true, URL: appsflyerURL, Header: "Authorization", ValuePrefix: "Bearer ", EnvVar: "KILO_MCP_APPSFLYER_TOKEN"}}
	data, _, err := mergeCodexMCP([]byte("model = 'x'\n"), servers)
	if err != nil {
		t.Fatal(err)
	}
	table := codexServer(t, data, "appsflyer")
	if table["bearer_token_env_var"] != "KILO_MCP_APPSFLYER_TOKEN" {
		t.Fatalf("expected bearer_token_env_var: %v", table)
	}
	if _, exists := table["http_headers"]; exists {
		t.Fatal("mode B must not write a literal header")
	}
	if bytes.Contains(data, []byte("Bearer ")) {
		t.Fatal("mode B must store no secret")
	}
}

func TestCodexMCPModeBEmptyPrefixUsesEnvHTTPHeaders(t *testing.T) {
	servers := []mcpServerConfig{{ID: "signalbeacon", Enabled: true, URL: "https://signalbeacon.mau.io/mcp", Header: "X-Api-Key", EnvVar: "KILO_MCP_SIGNALBEACON_TOKEN"}}
	data, _, err := mergeCodexMCP([]byte("model = 'x'\n"), servers)
	if err != nil {
		t.Fatal(err)
	}
	headers, _ := codexServer(t, data, "signalbeacon")["env_http_headers"].(map[string]any)
	if headers["X-Api-Key"] != "KILO_MCP_SIGNALBEACON_TOKEN" {
		t.Fatalf("expected env_http_headers: %v", headers)
	}
}

func TestCodexMCPModeBRefusesPrefixedNonBearer(t *testing.T) {
	servers := []mcpServerConfig{{ID: "x", Enabled: true, URL: "https://x.example/mcp", Header: "X-Api-Key", ValuePrefix: "Token ", EnvVar: "KILO_MCP_X_TOKEN"}}
	if _, _, err := mergeCodexMCP([]byte("model = 'x'\n"), servers); err == nil {
		t.Fatal("expected a refusal for an inexpressible mode-B header")
	}
}

func TestCodexMCPForeignCollisionIsSkipped(t *testing.T) {
	original := []byte("[mcp_servers.supermetrics]\ncommand = 'someone-elses'\n")
	servers := []mcpServerConfig{{ID: "supermetrics", Enabled: true, URL: supermetricsURL, Header: "Authorization", ValuePrefix: "Bearer ", Token: "x"}}
	data, skipped, err := mergeCodexMCP(original, servers)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || skipped[0] != "supermetrics" {
		t.Fatalf("expected a skip, got %v", skipped)
	}
	if !bytes.Equal(data, original) {
		t.Fatal("a foreign entry must never change")
	}
}

func TestCodexMCPRemoveOnlyOwnedEntry(t *testing.T) {
	original := []byte("[mcp_servers.docs]\ncommand = 'd'\n")
	on := []mcpServerConfig{{ID: "supermetrics", Enabled: true, URL: supermetricsURL, Header: "Authorization", ValuePrefix: "Bearer ", Token: "t"}}
	data, _, err := mergeCodexMCP(original, on)
	if err != nil {
		t.Fatal(err)
	}
	off := []mcpServerConfig{{ID: "supermetrics", Enabled: false, URL: supermetricsURL, Header: "Authorization", ValuePrefix: "Bearer "}}
	data, skipped, err := mergeCodexMCP(data, off)
	if err != nil || len(skipped) != 0 {
		t.Fatalf("remove failed: %v %v", err, skipped)
	}
	servers, _ := mustMCPTOML(t, data)["mcp_servers"].(map[string]any)
	if servers["supermetrics"] != nil || servers["docs"] == nil {
		t.Fatal("disable must remove only the owned entry")
	}
}

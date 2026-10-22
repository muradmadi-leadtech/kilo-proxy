package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func claudeMCP(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	servers, _ := value["mcpServers"].(map[string]any)
	return servers
}

func TestClaudeCodeMCPAddPreservesUnrelated(t *testing.T) {
	original := []byte("{\n  \"numStartups\": 7,\n  \"someOtherTopLevelKey\": { \"a\": 1 },\n  \"projects\": { \"/tmp/demo\": { \"allowedTools\": [\"Bash\"] } }\n}\n")
	servers := []mcpServerConfig{{ID: "supermetrics", Enabled: true, URL: supermetricsURL, Header: "Authorization", ValuePrefix: "Bearer ", Token: "api_abc"}}
	data, skipped, err := mergeClaudeCodeMCP(original, servers)
	if err != nil || len(skipped) != 0 {
		t.Fatalf("merge failed: %v (skipped %v)", err, skipped)
	}
	entry, _ := claudeMCP(t, data)["supermetrics"].(map[string]any)
	if entry["type"] != "http" || entry["url"] != supermetricsURL {
		t.Fatalf("bad entry: %v", entry)
	}
	headers, _ := entry["headers"].(map[string]any)
	if headers["Authorization"] != "Bearer api_abc" {
		t.Fatalf("mode A header missing: %v", entry)
	}
	// Unrelated keys and their original formatting survive.
	if !bytes.Contains(data, []byte("\"numStartups\": 7")) {
		t.Fatal("unrelated state was reformatted")
	}
	if !bytes.Contains(data, []byte("\"allowedTools\"")) {
		t.Fatal("unrelated project state was dropped")
	}
	again, _, err := mergeClaudeCodeMCP(data, servers)
	if err != nil || !bytes.Equal(data, again) {
		t.Fatalf("not idempotent: %v", err)
	}
}

func TestClaudeCodeMCPModeBEnvReference(t *testing.T) {
	servers := []mcpServerConfig{{ID: "appsflyer", Enabled: true, URL: appsflyerURL, Header: "Authorization", ValuePrefix: "Bearer ", EnvVar: "KILO_MCP_APPSFLYER_TOKEN"}}
	data, _, err := mergeClaudeCodeMCP([]byte("{}\n"), servers)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := claudeMCP(t, data)["appsflyer"].(map[string]any)
	headers, _ := entry["headers"].(map[string]any)
	if headers["Authorization"] != "Bearer ${KILO_MCP_APPSFLYER_TOKEN}" {
		t.Fatalf("expected an env reference, got %v", headers)
	}
	if bytes.Contains(data, []byte("eyJ")) {
		t.Fatal("mode B must store no secret")
	}
}

func TestClaudeCodeMCPCreatesContainerForSeveral(t *testing.T) {
	servers := []mcpServerConfig{
		{ID: "supermetrics", Enabled: true, URL: supermetricsURL, Header: "Authorization", ValuePrefix: "Bearer ", Token: "a"},
		{ID: "appsflyer", Enabled: true, URL: appsflyerURL, Header: "Authorization", ValuePrefix: "Bearer ", Token: "b"},
	}
	data, _, err := mergeClaudeCodeMCP([]byte("{\"numStartups\": 1}\n"), servers)
	if err != nil {
		t.Fatal(err)
	}
	parsed := claudeMCP(t, data)
	if parsed["supermetrics"] == nil || parsed["appsflyer"] == nil {
		t.Fatalf("both servers must be present: %v", parsed)
	}
}

func TestClaudeCodeMCPForeignCollisionIsSkipped(t *testing.T) {
	original := []byte("{\n  \"mcpServers\": {\n    \"supermetrics\": { \"type\": \"http\", \"url\": \"https://elsewhere.example/mcp\" }\n  }\n}\n")
	servers := []mcpServerConfig{{ID: "supermetrics", Enabled: true, URL: supermetricsURL, Header: "Authorization", ValuePrefix: "Bearer ", Token: "x"}}
	data, skipped, err := mergeClaudeCodeMCP(original, servers)
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

func TestClaudeCodeMCPRemoveOnlyOwnedEntry(t *testing.T) {
	original := []byte("{\n  \"mcpServers\": { \"their\": { \"type\": \"http\", \"url\": \"https://their.example/mcp\" } }\n}\n")
	on := []mcpServerConfig{{ID: "supermetrics", Enabled: true, URL: supermetricsURL, Header: "Authorization", ValuePrefix: "Bearer ", Token: "t"}}
	data, _, err := mergeClaudeCodeMCP(original, on)
	if err != nil {
		t.Fatal(err)
	}
	off := []mcpServerConfig{{ID: "supermetrics", Enabled: false, URL: supermetricsURL, Header: "Authorization", ValuePrefix: "Bearer "}}
	data, _, err = mergeClaudeCodeMCP(data, off)
	if err != nil {
		t.Fatal(err)
	}
	servers := claudeMCP(t, data)
	if servers["supermetrics"] != nil || servers["their"] == nil {
		t.Fatal("disable must remove only the owned entry")
	}
}

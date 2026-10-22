//go:build desktop

package main

import (
	"testing"
)

func mcpTestServer() mcpServerView {
	return mcpServerView{
		ID: "supermetrics", Name: mcpText{EN: "Supermetrics", ES: "Supermetrics"},
		Description: mcpText{EN: "Marketing data", ES: "Datos de marketing"},
		URL:         "https://mcp.supermetrics.com/mcp", Auth: "token", Modes: []string{"A", "B"},
		EnvVar: "KILO_MCP_SUPERMETRICS_TOKEN", DocsURL: "https://support.supermetrics.com/",
	}
}

func TestNativeMCPStatusReflectsReadiness(t *testing.T) {
	u := nativeTestUI(t)
	u.language = "en"
	base := mcpTestServer()

	for _, test := range []struct {
		name   string
		mutate func(*mcpServerView)
		want   string
	}{
		{"token without a credential needs one", func(s *mcpServerView) { s.Enabled = true }, "Add a credential to enable it"},
		{"token with a credential is ready", func(s *mcpServerView) { s.Enabled, s.HasToken = true, true }, "Ready"},
		{"disabled is off", func(s *mcpServerView) { s.HasToken = true }, "Disabled"},
		{"oauth needs a sign-in", func(s *mcpServerView) { s.Auth, s.Enabled = "oauth", true }, "Sign in to enable it"},
		{"oauth with a sign-in is ready", func(s *mcpServerView) { s.Auth, s.Enabled, s.Connected = "oauth", true, true }, "Signed in"},
		{"open server is ready", func(s *mcpServerView) { s.Auth, s.Enabled = "none", true }, "Ready"},
	} {
		s := base
		test.mutate(&s)
		if got := u.mcpStatus(s); got != test.want {
			t.Fatalf("%s: got %q, want %q", test.name, got, test.want)
		}
	}
}

func TestNativeMCPLabelsAreBilingual(t *testing.T) {
	u := nativeTestUI(t)
	u.language = "en"
	if u.mcpAuthLabel("oauth") != "OAuth (coming soon)" || u.mcpModeLabel("B") != "Injected at launch · no secret stored" {
		t.Fatal("English labels are wrong")
	}
	u.language = "es"
	if u.mcpAuthLabel("none") != "Sin credencial" || u.mcpModeLabel("A") != "Valor literal en la configuración" {
		t.Fatal("Spanish labels are wrong")
	}
}

func TestNativeMCPVisibleServersFiltersBySearch(t *testing.T) {
	u := nativeTestUI(t)
	u.language = "en"
	second := mcpTestServer()
	second.ID, second.Name = "appsflyer", mcpText{EN: "AppsFlyer", ES: "AppsFlyer"}
	second.Description = mcpText{EN: "Mobile attribution", ES: "Atribución móvil"}
	u.mcpState().servers = []mcpServerView{mcpTestServer(), second}

	if got := len(u.mcpVisibleServers()); got != 2 {
		t.Fatalf("no query must show every server, got %d", got)
	}
	u.setValue("mcp.search", "attrib") // case-insensitive substring over the description
	visible := u.mcpVisibleServers()
	if len(visible) != 1 || visible[0].ID != "appsflyer" {
		t.Fatalf("description search failed: %+v", visible)
	}
	u.setValue("mcp.search", "SUPERMETRICS")
	if visible = u.mcpVisibleServers(); len(visible) != 1 || visible[0].ID != "supermetrics" {
		t.Fatalf("id search must be case-insensitive: %+v", visible)
	}
	u.setValue("mcp.search", "nothing-matches")
	if visible = u.mcpVisibleServers(); len(visible) != 0 {
		t.Fatalf("unmatched query must show nothing: %+v", visible)
	}
}

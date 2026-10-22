package main

import (
	"encoding/json"
	"testing"
)

func baseCatalogServer() curatedMCP {
	return curatedMCP{
		ID: "alpha", Name: mcpText{EN: "Alpha", ES: "Alfa"}, Description: mcpText{EN: "desc", ES: "desc"},
		URL: "https://mcp.example.com/mcp", Transport: "http", Auth: "token",
		Header: "Authorization", ValuePrefix: "Bearer ", EnvVar: "KILO_MCP_ALPHA_TOKEN", Modes: []string{"A", "B"},
	}
}

func catalogJSON(t *testing.T, servers ...curatedMCP) []byte {
	t.Helper()
	data, err := json.Marshal(curatedMCPCatalog{SchemaVersion: 1, Servers: servers})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestEmbeddedMCPCatalogIsValid(t *testing.T) {
	servers, err := parseMCPCatalog(embeddedMCPCatalog)
	if err != nil {
		t.Fatalf("embedded catalog must parse: %v", err)
	}
	if len(servers) == 0 {
		t.Fatal("embedded catalog is empty")
	}
	if builtinMCPCatalog() == nil {
		t.Fatal("builtinMCPCatalog must load the embedded file")
	}
	for _, s := range servers {
		if s.Auth == "token" && s.EnvVar == "" {
			t.Fatalf("token server %q needs a mode-B env var", s.ID)
		}
		if s.EnvVar != "" && !mcpEnvVarPattern.MatchString(s.EnvVar) {
			t.Fatalf("token server %q has a bad env var %q", s.ID, s.EnvVar)
		}
	}
}

func TestParseMCPCatalogAcceptsBase(t *testing.T) {
	if _, err := parseMCPCatalog(catalogJSON(t, baseCatalogServer())); err != nil {
		t.Fatalf("base server must parse: %v", err)
	}
	// OAuth is mode B only: the config names a launch variable, never the token.
	oauth := baseCatalogServer()
	oauth.ID, oauth.Auth, oauth.Modes = "beta", "oauth", []string{"B"}
	oauth.Header, oauth.ValuePrefix, oauth.EnvVar = "Authorization", "Bearer ", "KILO_MCP_BETA_TOKEN"
	if _, err := parseMCPCatalog(catalogJSON(t, oauth)); err != nil {
		t.Fatalf("oauth server must parse: %v", err)
	}
}

func TestParseMCPCatalogRejects(t *testing.T) {
	cases := map[string]func() []byte{
		"bad id": func() []byte {
			s := baseCatalogServer()
			s.ID = "Bad_ID"
			return catalogJSON(t, s)
		},
		"duplicate id": func() []byte { return catalogJSON(t, baseCatalogServer(), baseCatalogServer()) },
		"insecure url": func() []byte {
			s := baseCatalogServer()
			s.URL = "http://mcp.example.com/mcp"
			return catalogJSON(t, s)
		},
		"url with query": func() []byte {
			s := baseCatalogServer()
			s.URL = "https://mcp.example.com/mcp?x=1"
			return catalogJSON(t, s)
		},
		"credential-ish env var": func() []byte {
			s := baseCatalogServer()
			s.EnvVar = "SUPERMETRICS_API_KEY"
			return catalogJSON(t, s)
		},
		"mode B without env var": func() []byte {
			s := baseCatalogServer()
			s.EnvVar = ""
			return catalogJSON(t, s)
		},
		"token without header": func() []byte {
			s := baseCatalogServer()
			s.Header = ""
			return catalogJSON(t, s)
		},
		"oauth without a launch variable": func() []byte {
			s := baseCatalogServer()
			s.Auth, s.EnvVar, s.Modes = "oauth", "", []string{"B"}
			return catalogJSON(t, s)
		},
		"oauth with mode A": func() []byte {
			s := baseCatalogServer()
			s.Auth, s.Modes = "oauth", []string{"A", "B"}
			return catalogJSON(t, s)
		},
		"oauth without a header": func() []byte {
			s := baseCatalogServer()
			s.Auth, s.Header, s.Modes = "oauth", "", []string{"B"}
			return catalogJSON(t, s)
		},
		"invalid oauth override": func() []byte {
			s := baseCatalogServer()
			s.Auth, s.Modes, s.TokenURL = "oauth", []string{"B"}, "http://insecure.example/token"
			return catalogJSON(t, s)
		},
		"empty name": func() []byte {
			s := baseCatalogServer()
			s.Name.ES = ""
			return catalogJSON(t, s)
		},
		"duplicate env var": func() []byte {
			a, b := baseCatalogServer(), baseCatalogServer()
			b.ID = "gamma"
			return catalogJSON(t, a, b)
		},
		"unknown field": func() []byte {
			return []byte(`{"schemaVersion":1,"servers":[{"id":"alpha","name":{"en":"A","es":"A"},"description":{"en":"d","es":"d"},"url":"https://x/mcp","transport":"http","auth":"token","header":"Authorization","modes":["A"],"nope":1}]}`)
		},
		"bad schema": func() []byte {
			return []byte(`{"schemaVersion":2,"servers":[]}`)
		},
	}
	for name, build := range cases {
		if _, err := parseMCPCatalog(build()); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
}

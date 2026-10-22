package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MCP OAuth (spec 2025-06-18): Protected Resource Metadata (RFC 9728), then
// Authorization Server Metadata (RFC 8414), then Dynamic Client Registration,
// then authorization code + PKCE S256 on a loopback redirect the user's browser
// can reach. Access tokens expire, so an OAuth server is only ever delivered as
// mode B: Kilo keeps the token in the vault and injects a fresh value at launch.
const (
	mcpOAuthRequestLimit = 1 << 20
	mcpOAuthStepTimeout  = 20 * time.Second
	mcpOAuthFlowTimeout  = 5 * time.Minute
	mcpOAuthRefreshSkew  = 60 * time.Second
	mcpOAuthSecretMax    = 4096
)

// Stored in the vault under mcp-<id>-oauth. Never written into a harness config.
type mcpOAuthCredentials struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret,omitempty"`
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken,omitempty"`
	TokenType    string `json:"tokenType,omitempty"`
	Scope        string `json:"scope,omitempty"`
	ExpiresAt    int64  `json:"expiresAt,omitempty"` // unix seconds; 0 means unknown
	TokenURL     string `json:"tokenUrl"`
	Resource     string `json:"resource,omitempty"`
	Issuer       string `json:"issuer,omitempty"`
}

func mcpOAuthSlot(id string) string { return "mcp-" + id + "-oauth" }

func validMCPOAuthCredentials(c mcpOAuthCredentials) bool {
	short := func(value string, limit int) bool {
		return len(value) <= limit && !strings.ContainsAny(value, "\x00\r\n")
	}
	return c.ClientID != "" && c.AccessToken != "" && short(c.ClientID, mcpOAuthSecretMax) && short(c.AccessToken, mcpOAuthSecretMax) &&
		short(c.RefreshToken, mcpOAuthSecretMax) && short(c.TokenURL, 2048) && short(c.Resource, 2048) && validMCPUURL(c.TokenURL) &&
		(c.RefreshToken != "" || c.ExpiresAt == 0 || time.Now().Unix() < c.ExpiresAt)
}

func mcpOAuthJSON(credentials mcpOAuthCredentials) (string, error) {
	data, err := json.Marshal(credentials)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (a *app) mcpOAuthCredentials(id string) (mcpOAuthCredentials, bool) {
	raw, err := a.vault.Get(mcpOAuthSlot(id))
	if err != nil || raw == "" {
		return mcpOAuthCredentials{}, false
	}
	var credentials mcpOAuthCredentials
	if json.Unmarshal([]byte(raw), &credentials) != nil || !validMCPOAuthCredentials(credentials) {
		return mcpOAuthCredentials{}, false
	}
	return credentials, true
}

func (a *app) mcpOAuthConnected(id string) bool {
	_, ok := a.mcpOAuthCredentials(id)
	return ok
}

// --- discovery ---------------------------------------------------------------

type mcpProtectedResource struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
}

type mcpAuthorizationServer struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	RegistrationEndpoint          string   `json:"registration_endpoint"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
	ScopesSupported               []string `json:"scopes_supported"`
}

func mcpJSONGet(ctx context.Context, transport http.RoundTripper, endpoint string, target any) error {
	step, cancel := context.WithTimeout(ctx, mcpOAuthStepTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(step, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Transport: transport, Timeout: mcpOAuthStepTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, mcpOAuthRequestLimit+1))
	if err != nil || len(body) > mcpOAuthRequestLimit {
		return errors.New("respuesta demasiado grande")
	}
	return json.Unmarshal(body, target)
}

// mcpOAuthMetadata resolves the authorization server for a server URL. Both RFC
// 9728 well-known placements are tried, since deployments differ.
func mcpOAuthMetadata(ctx context.Context, transport http.RoundTripper, server curatedMCP) (mcpAuthorizationServer, error) {
	base, err := url.Parse(server.URL)
	if err != nil {
		return mcpAuthorizationServer{}, errors.New("dirección de servidor no válida")
	}
	// Catalog overrides skip discovery for servers that do not publish metadata.
	if server.AuthorizationURL != "" && server.TokenURL != "" {
		return mcpAuthorizationServer{
			Issuer: server.URL, AuthorizationEndpoint: server.AuthorizationURL,
			TokenEndpoint: server.TokenURL, RegistrationEndpoint: server.RegistrationURL,
			CodeChallengeMethodsSupported: []string{"S256"},
		}, nil
	}
	var resource mcpProtectedResource
	candidates := []string{
		base.Scheme + "://" + base.Host + "/.well-known/oauth-protected-resource" + strings.TrimSuffix(base.Path, "/"),
		strings.TrimSuffix(server.URL, "/") + "/.well-known/oauth-protected-resource",
	}
	found := false
	for _, candidate := range candidates {
		if err := mcpJSONGet(ctx, transport, candidate, &resource); err == nil && len(resource.AuthorizationServers) > 0 {
			found = true
			break
		}
	}
	if !found {
		return mcpAuthorizationServer{}, errors.New("el servidor no publica metadatos OAuth")
	}
	var lastErr error
	for _, issuer := range resource.AuthorizationServers {
		if !validMCPUURL(issuer) {
			lastErr = errors.New("el servidor anunció un servidor de autorización no utilizable")
			continue
		}
		trimmed := strings.TrimSuffix(issuer, "/")
		for _, candidate := range []string{
			trimmed + "/.well-known/oauth-authorization-server",
			trimmed + "/.well-known/openid-configuration",
		} {
			var metadata mcpAuthorizationServer
			if err := mcpJSONGet(ctx, transport, candidate, &metadata); err != nil {
				lastErr = err
				continue
			}
			if !validMCPUURL(metadata.AuthorizationEndpoint) || !validMCPUURL(metadata.TokenEndpoint) {
				lastErr = errors.New("el servidor de autorización publicó endpoints no utilizables")
				continue
			}
			if len(metadata.CodeChallengeMethodsSupported) > 0 && !containsString(metadata.CodeChallengeMethodsSupported, "S256") {
				lastErr = errors.New("el servidor de autorización no admite PKCE S256")
				continue
			}
			if metadata.Issuer == "" {
				metadata.Issuer = issuer
			}
			return metadata, nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("el servidor de autorización no está disponible")
	}
	return mcpAuthorizationServer{}, lastErr
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}

// --- PKCE and client registration --------------------------------------------

func mcpPKCEPair() (verifier, challenge string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func mcpRandomState() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// mcpRegisterClient performs Dynamic Client Registration, or returns the hosted
// client-metadata document URL when the server prefers CIMD.
func mcpRegisterClient(ctx context.Context, transport http.RoundTripper, server curatedMCP, metadata mcpAuthorizationServer, redirectURI string) (id, secret string, err error) {
	if server.ClientMetadataURL != "" && validMCPUURL(server.ClientMetadataURL) {
		return server.ClientMetadataURL, "", nil
	}
	if !validMCPUURL(metadata.RegistrationEndpoint) {
		return "", "", errors.New("el servidor de autorización no admite registro de cliente")
	}
	body, err := json.Marshal(map[string]any{
		"client_name": "Kilo Proxy", "redirect_uris": []string{redirectURI},
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	if err != nil {
		return "", "", err
	}
	step, cancel := context.WithTimeout(ctx, mcpOAuthStepTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(step, http.MethodPost, metadata.RegistrationEndpoint, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Transport: transport, Timeout: mcpOAuthStepTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, mcpOAuthRequestLimit+1))
	if err != nil || len(raw) > mcpOAuthRequestLimit {
		return "", "", errors.New("la respuesta de registro no fue utilizable")
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("el registro del cliente falló con HTTP %d", resp.StatusCode)
	}
	var registered struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if json.Unmarshal(raw, &registered) != nil || registered.ClientID == "" || len(registered.ClientID) > mcpOAuthSecretMax || strings.ContainsAny(registered.ClientID, "\x00\r\n") {
		return "", "", errors.New("el servidor de autorización no devolvió un identificador de cliente")
	}
	if len(registered.ClientSecret) > mcpOAuthSecretMax || strings.ContainsAny(registered.ClientSecret, "\x00\r\n") {
		return "", "", errors.New("el servidor de autorización devolvió un secreto de cliente no utilizable")
	}
	return registered.ClientID, registered.ClientSecret, nil
}

// --- token endpoint ----------------------------------------------------------

func mcpTokenRequest(ctx context.Context, transport http.RoundTripper, tokenURL string, form url.Values, clientID, clientSecret string) (mcpOAuthCredentials, error) {
	if clientSecret != "" {
		// Servers that issue a secret expect client_secret_post by default.
		form.Set("client_id", clientID)
		form.Set("client_secret", clientSecret)
	} else {
		form.Set("client_id", clientID)
	}
	step, cancel := context.WithTimeout(ctx, mcpOAuthStepTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(step, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return mcpOAuthCredentials{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Transport: transport, Timeout: mcpOAuthStepTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return mcpOAuthCredentials{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, mcpOAuthRequestLimit+1))
	if err != nil || len(raw) > mcpOAuthRequestLimit {
		return mcpOAuthCredentials{}, errors.New("la respuesta del token no fue utilizable")
	}
	if resp.StatusCode != http.StatusOK {
		return mcpOAuthCredentials{}, fmt.Errorf("el endpoint de token respondió HTTP %d", resp.StatusCode)
	}
	var exchanged struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		Scope        string `json:"scope"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if json.Unmarshal(raw, &exchanged) != nil || exchanged.AccessToken == "" || len(exchanged.AccessToken) > mcpOAuthSecretMax || strings.ContainsAny(exchanged.AccessToken, "\x00\r\n") {
		return mcpOAuthCredentials{}, errors.New("el endpoint de token no devolvió un token de acceso")
	}
	credentials := mcpOAuthCredentials{
		ClientID: clientID, ClientSecret: clientSecret,
		AccessToken: exchanged.AccessToken, RefreshToken: exchanged.RefreshToken,
		TokenType: exchanged.TokenType, Scope: exchanged.Scope, TokenURL: tokenURL,
	}
	if exchanged.ExpiresIn > 0 {
		credentials.ExpiresAt = time.Now().Add(time.Duration(exchanged.ExpiresIn) * time.Second).Unix()
	}
	if !validMCPOAuthCredentials(credentials) {
		return mcpOAuthCredentials{}, errors.New("el endpoint de token devolvió credenciales no utilizables")
	}
	return credentials, nil
}

// mcpOAuthAccessToken returns a usable access token, refreshing it when needed.
// A refresh failure with no usable token is reported so callers can skip the
// server instead of writing a stale value.
func (a *app) mcpOAuthAccessToken(ctx context.Context, id string) (string, error) {
	credentials, ok := a.mcpOAuthCredentials(id)
	if !ok {
		return "", errors.New("no hay inicio de sesión guardado para este servidor")
	}
	if credentials.ExpiresAt == 0 || time.Now().Add(mcpOAuthRefreshSkew).Unix() < credentials.ExpiresAt {
		return credentials.AccessToken, nil
	}
	if credentials.RefreshToken == "" {
		return "", errors.New("el inicio de sesión guardado caducó; vuelve a iniciarlo")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {credentials.RefreshToken}}
	if credentials.Resource != "" {
		form.Set("resource", credentials.Resource)
	}
	refreshed, err := mcpTokenRequest(ctx, a.transport, credentials.TokenURL, form, credentials.ClientID, credentials.ClientSecret)
	if err != nil {
		return "", err
	}
	// A refresh response may omit the refresh token; keep the previous one.
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = credentials.RefreshToken
	}
	refreshed.Resource, refreshed.Issuer = credentials.Resource, credentials.Issuer
	encoded, err := mcpOAuthJSON(refreshed)
	if err != nil {
		return "", err
	}
	if err := a.vault.Set(mcpOAuthSlot(id), encoded); err != nil {
		return "", errors.New("no se puede guardar el inicio de sesión renovado")
	}
	return refreshed.AccessToken, nil
}

// --- the interactive flow ----------------------------------------------------

type mcpOAuthSession struct {
	Server           string    `json:"server"`
	Status           string    `json:"status"` // starting | pending | connected | error | cancelled
	AuthorizationURL string    `json:"authorizationUrl,omitempty"`
	Message          string    `json:"message,omitempty"`
	ExpiresAt        time.Time `json:"expiresAt"`
	cancel           context.CancelFunc
}

func (a *app) mcpOAuthPendingLocked() bool {
	return a.mcpOAuthFlow != nil && (a.mcpOAuthFlow.Status == "starting" || a.mcpOAuthFlow.Status == "pending")
}

func (a *app) cancelMCPOAuthLocked() {
	if a.mcpOAuthPendingLocked() {
		a.mcpOAuthFlow.Status = "cancelled"
		a.mcpOAuthFlow.AuthorizationURL = ""
		a.mcpOAuthFlow.cancel()
	}
}

func (a *app) finishMCPOAuth(session *mcpOAuthSession, status, message string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.mcpOAuthFlow != session || !a.mcpOAuthPendingLocked() {
		return
	}
	session.Status, session.Message, session.AuthorizationURL = status, message, ""
	session.cancel()
}

// beginMCPOAuth starts the flow and returns the URL the user must open. It never
// opens a browser itself, matching the Kilo and ChatGPT device flows.
func (a *app) beginMCPOAuth(server curatedMCP) (mcpOAuthSession, error) {
	a.mu.Lock()
	select {
	case <-a.quit:
		a.mu.Unlock()
		return mcpOAuthSession{}, errors.New("La aplicación se está cerrando.")
	default:
	}
	if a.mcpOAuthPendingLocked() {
		a.mu.Unlock()
		return mcpOAuthSession{}, errors.New("Ya hay un inicio de sesión MCP en curso. Cancélalo antes de empezar otro.")
	}
	ctx, cancel := context.WithCancel(context.Background())
	session := &mcpOAuthSession{Server: server.ID, Status: "starting", ExpiresAt: time.Now().Add(mcpOAuthFlowTimeout), cancel: cancel}
	a.mcpOAuthFlow = session
	a.mu.Unlock()
	go a.runMCPOAuth(ctx, session, server)
	return *session, nil
}

// runMCPOAuth performs discovery, registration,a loopback listener and the code
// exchange. The browser is opened by the caller from the returned URL.
func (a *app) runMCPOAuth(ctx context.Context, session *mcpOAuthSession, server curatedMCP) {
	fail := func(message string) { a.finishMCPOAuth(session, "error", message) }
	ctx, cancel := context.WithTimeout(ctx, mcpOAuthFlowTimeout)
	defer cancel()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		fail("No se pudo abrir el puerto local para completar el inicio de sesión.")
		return
	}
	defer listener.Close()
	redirectURI := "http://" + listener.Addr().String() + "/callback"
	metadata, err := mcpOAuthMetadata(ctx, a.transport, server)
	if err != nil {
		fail("No se pudo preparar el inicio de sesión: " + err.Error() + ".")
		return
	}
	clientID, clientSecret, err := mcpRegisterClient(ctx, a.transport, server, metadata, redirectURI)
	if err != nil {
		fail("No se pudo registrar este equipo: " + err.Error() + ".")
		return
	}
	verifier, challenge, err := mcpPKCEPair()
	if err != nil {
		fail("No se pudo preparar la solicitud segura de inicio de sesión.")
		return
	}
	state, err := mcpRandomState()
	if err != nil {
		fail("No se pudo preparar la solicitud segura de inicio de sesión.")
		return
	}
	authorize, err := url.Parse(metadata.AuthorizationEndpoint)
	if err != nil {
		fail("El servidor publicó una dirección de autorización no válida.")
		return
	}
	query := authorize.Query()
	query.Set("response_type", "code")
	query.Set("client_id", clientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("state", state)
	query.Set("resource", server.URL)
	if scope := mcpOAuthScope(server, metadata); scope != "" {
		query.Set("scope", scope)
	}
	authorize.RawQuery = query.Encode()

	type callback struct{ code, state, failure string }
	results := make(chan callback, 1)
	httpServer := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	httpServer.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query()
		result := callback{code: query.Get("code"), state: query.Get("state"), failure: query.Get("error")}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		message := "Ya puedes cerrar esta pestaña y volver a Kilo Proxy."
		if result.failure != "" || result.code == "" {
			message = "No se completó el inicio de sesión. Vuelve a intentarlo desde Kilo Proxy."
		}
		fmt.Fprint(w, mcpOAuthResultPage(message))
		select {
		case results <- result:
		default:
		}
	})
	go httpServer.Serve(listener)
	defer httpServer.Close()

	a.mu.Lock()
	if a.mcpOAuthFlow != session || session.Status != "starting" {
		a.mu.Unlock()
		return
	}
	session.Status, session.AuthorizationURL = "pending", authorize.String()
	a.mu.Unlock()

	var result callback
	select {
	case result = <-results:
	case <-ctx.Done():
		return
	}
	if result.failure != "" || result.code == "" || result.state != state {
		fail("El inicio de sesión no se completó o la respuesta no era válida.")
		return
	}
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {result.code},
		"redirect_uri": {redirectURI}, "code_verifier": {verifier}, "resource": {server.URL},
	}
	credentials, err := mcpTokenRequest(ctx, a.transport, metadata.TokenEndpoint, form, clientID, clientSecret)
	if err != nil {
		fail("No se pudo completar el inicio de sesión: " + err.Error() + ".")
		return
	}
	credentials.ClientSecret, credentials.Resource, credentials.Issuer = clientSecret, server.URL, metadata.Issuer
	encoded, err := mcpOAuthJSON(credentials)
	if err != nil {
		fail("No se pudieron guardar las credenciales del inicio de sesión.")
		return
	}
	if err := a.vault.Set(mcpOAuthSlot(server.ID), encoded); err != nil {
		fail("No se pueden guardar las credenciales en el almacén local.")
		return
	}
	a.finishMCPOAuth(session, "connected", "")
}

// mcpOAuthScope prefers a curated scope list, then the server's own default.
func mcpOAuthScope(server curatedMCP, metadata mcpAuthorizationServer) string {
	if len(server.Scopes) > 0 {
		return strings.Join(server.Scopes, " ")
	}
	return ""
}

func mcpOAuthResultPage(message string) string {
	return "<!doctype html><html lang=\"es\"><head><meta charset=\"utf-8\"><title>Kilo Proxy</title>" +
		"<style>body{font:15px -apple-system,BlinkMacSystemFont,\"Segoe UI\",sans-serif;display:grid;place-items:center;height:100vh;margin:0;background:#f4f5ef;color:#252b28}" +
		"div{max-width:420px;text-align:center}</style></head><body><div><h1>Kilo Proxy</h1><p>" +
		html.EscapeString(message) + "</p></div></body></html>"
}

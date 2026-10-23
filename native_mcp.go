//go:build desktop

package main

import (
	"encoding/json"
	"strings"

	"gioui.org/font"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// One server as the catalog endpoint returns it. The token itself never
// arrives here; only whether one is stored.
type mcpServerView struct {
	ID          string   `json:"id"`
	Name        mcpText  `json:"name"`
	Description mcpText  `json:"description"`
	URL         string   `json:"url"`
	Auth        string   `json:"auth"`
	Modes       []string `json:"modes"`
	EnvVar      string   `json:"envVar"`
	DocsURL     string   `json:"docsUrl"`
	TokenURL    string   `json:"tokenUrl"`
	Scopes      []string `json:"scopes"`
	Enabled     bool     `json:"enabled"`
	Mode        string   `json:"mode"`
	Harnesses   []string `json:"harnesses"`
	HasToken    bool     `json:"hasToken"`
	Connected   bool     `json:"connected"`
}

type nativeMCPUI struct {
	started   bool
	loaded    bool
	oauth     string // server id with a sign-in in progress, from /api/state
	oauthSeen string // last status applied, so a transition reloads the catalog once
	servers   []mcpServerView
	harnesses []mcpHarness
	warning   string
	error     string
	results   []mcpSyncReport
	synced    bool
}

// mcpPad gives each stacked block the same gap used by other pages.
func mcpPad(child layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Bottom: 16}.Layout(gtx, child)
	}
}

func (u *nativeUI) mcpState() *nativeMCPUI {
	if u.mcp == nil {
		u.mcp = &nativeMCPUI{}
	}
	return u.mcp
}

func (u *nativeUI) mcpName(s mcpServerView) string { return u.tr(s.Name.EN, s.Name.ES) }
func (u *nativeUI) mcpDescription(s mcpServerView) string {
	return u.tr(s.Description.EN, s.Description.ES)
}

func (u *nativeUI) mcpAuthLabel(auth string) string {
	switch auth {
	case "token":
		return u.tr("Token", "Token")
	case "none":
		return u.tr("No credential", "Sin credencial")
	case "oauth":
		return u.tr("OAuth (coming soon)", "OAuth (próximamente)")
	}
	return auth
}

func (u *nativeUI) mcpModeLabel(mode string) string {
	switch mode {
	case "A":
		return u.tr("Literal value in the config", "Valor literal en la configuración")
	case "B":
		return u.tr("Injected at launch · no secret stored", "Inyectado al abrir · sin guardar secreto")
	}
	return ""
}

// mcpStatus summarizes a server in one line and whether it is ready to sync.
func (u *nativeUI) mcpStatus(s mcpServerView) string {
	switch {
	case s.Auth == "oauth" && !s.Connected:
		return u.tr("Sign in to enable it", "Inicia sesión para activarlo")
	case s.Auth == "oauth":
		return u.tr("Signed in", "Sesión iniciada")
	case !s.Enabled:
		return u.tr("Disabled", "Desactivado")
	case s.Auth == "token" && !s.HasToken:
		return u.tr("Add a credential to enable it", "Añade una credencial para activarlo")
	default:
		return u.tr("Ready", "Listo")
	}
}

// mcpVisibleServers filters the catalog by the search box (id, name, description).
func (u *nativeUI) mcpVisibleServers() []mcpServerView {
	s := u.mcpState()
	query := strings.ToLower(strings.TrimSpace(u.value("mcp.search")))
	if query == "" {
		return s.servers
	}
	var visible []mcpServerView
	for _, server := range s.servers {
		haystack := strings.ToLower(server.ID + " " + server.Name.EN + " " + server.Name.ES + " " + server.Description.EN + " " + server.Description.ES)
		if strings.Contains(haystack, query) {
			visible = append(visible, server)
		}
	}
	return visible
}

func (u *nativeUI) mcpLoad() {
	if u.busy["GET/api/mcp/catalog"] {
		return
	}
	s := u.mcpState()
	s.started, s.error = true, ""
	u.call("GET", "/api/mcp/catalog", nil, func(raw json.RawMessage) {
		var payload struct {
			Servers   []mcpServerView `json:"servers"`
			Harnesses []mcpHarness    `json:"harnesses"`
			Warning   string          `json:"warning"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			s.error = u.tr("Cannot read the MCP catalog.", "No se puede leer el catálogo MCP.")
			u.invalidate()
			return
		}
		s.servers, s.harnesses, s.warning, s.loaded = payload.Servers, payload.Harnesses, payload.Warning, true
		u.invalidate()
	})
}

// mcpSetState posts one toggle/mode change and refreshes the catalog view.
func (u *nativeUI) mcpSetState(id string, fields map[string]any) {
	body := map[string]any{"id": id}
	for key, value := range fields {
		body[key] = value
	}
	u.call("POST", "/api/mcp/state", body, func(json.RawMessage) { u.mcpLoad() })
}

func (u *nativeUI) mcpSaveToken(s mcpServerView) {
	token := strings.TrimSpace(u.value("mcp.token." + s.ID))
	if token == "" {
		u.setNotice(nativeToneWarning, u.tr("Enter a token before saving.", "Introduce un token antes de guardar."))
		return
	}
	u.call("POST", "/api/mcp/auth", map[string]any{"id": s.ID, "token": token}, func(json.RawMessage) {
		u.setValue("mcp.token."+s.ID, "")
		u.setNotice(nativeToneSuccess, u.tr("Credential saved.", "Credencial guardada."))
		u.mcpLoad()
	})
}

func (u *nativeUI) mcpClearToken(id string) {
	u.call("DELETE", "/api/mcp/auth", map[string]any{"id": id}, func(json.RawMessage) {
		u.setNotice(nativeToneSuccess, u.tr("Stored credential removed.", "Credencial guardada eliminada."))
		u.mcpLoad()
	})
}

// mcpSignIn starts the OAuth flow and opens the authorization page. Kilo never
// opens a browser on its own; the returned URL is handed to the system.
func (u *nativeUI) mcpSignIn(id string) {
	u.call("POST", "/api/mcp/oauth/start", map[string]any{"id": id}, func(raw json.RawMessage) {
		var session struct {
			AuthorizationURL string `json:"authorizationUrl"`
			Status           string `json:"status"`
		}
		if json.Unmarshal(raw, &session) != nil || session.AuthorizationURL == "" {
			// A slow discovery step returns "starting"; /api/state reports the URL
			// through the sign-in button once it is ready, so do not report a
			// failure here.
			u.setNotice(nativeToneInfo, u.tr("Preparing the sign-in…", "Preparando el inicio de sesión…"))
			return
		}
		u.open(session.AuthorizationURL)
	})
}

func (u *nativeUI) mcpCancelSignIn() {
	u.call("POST", "/api/mcp/oauth/cancel", map[string]any{}, func(json.RawMessage) {
		u.mcpState().oauth = ""
		u.setNotice(nativeToneNeutral, u.tr("Sign-in cancelled.", "Inicio de sesión cancelado."))
		u.mcpLoad()
	})
}

func (u *nativeUI) mcpSync() {
	u.call("POST", "/api/mcp/sync", map[string]any{}, func(raw json.RawMessage) {
		var payload struct {
			Results []mcpSyncReport `json:"results"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			u.noticeError(err)
			return
		}
		s := u.mcpState()
		s.results, s.synced = payload.Results, true
		u.setNotice(nativeToneSuccess, u.tr("MCP settings synchronized.", "Ajustes MCP sincronizados."))
		u.mcpLoad()
	})
}

// mcpTextField is a bordered single-line input for a pasted credential.
func (u *nativeUI) mcpTextField(id, placeholder string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		e := u.editor(id)
		e.SingleLine, e.ReadOnly = true, false
		border, width, background := nativeBorderStrong, nativeLine, nativeSurface
		if gtx.Focused(e) {
			border, width = nativeInk, nativeLineHeavy
		}
		if !gtx.Enabled() {
			background = nativeSurfaceAlt
		}
		return nativeBox(gtx, background, func(gtx layout.Context) layout.Dimensions {
			return widget.Border{Color: border, Width: width}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				gtx.Constraints.Min.Y = max(gtx.Constraints.Min.Y, gtx.Dp(36))
				return layout.Inset{Top: 8, Bottom: 8, Left: 10, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.Y = 0
					semantic.LabelOp(placeholder).Add(gtx.Ops)
					style := material.Editor(u.theme, e, placeholder)
					style.Color, style.HintColor, style.TextSize = nativeText, nativeTextSubtle, 14
					return style.Layout(gtx)
				})
			})
		})
	}
}

func (u *nativeUI) mcpServerCard(s mcpServerView) layout.Widget {
	toggleLabel := u.tr("Enabled", "Activado")
	if !s.Enabled {
		toggleLabel = u.tr("Disabled", "Desactivado")
	}
	rows := []layout.Widget{
		u.uxsRow(u.tr("Status", "Estado"), u.mcpDescription(s), u.uxsValue(u.textStyle(14, u.mcpStatus(s), nativeText, font.Medium), s.URL), u.uxsToggle("mcp.toggle."+s.ID, toggleLabel, "", s.Enabled, func(on bool) {
			u.mcpSetState(s.ID, map[string]any{"enabled": on})
		}), nil),
	}
	if s.Auth == "token" {
		credential := u.mcpTextField("mcp.token."+s.ID, u.tr("Paste the token", "Pega el token"))
		var actions layout.Widget
		if s.HasToken {
			credential = u.uxsValue(u.textStyle(14, u.tr("Stored in the credential vault", "Guardada en el almacén de credenciales"), nativeText, font.Normal))
			actions = u.uxsActions(u.button("mcp.clear."+s.ID, u.tr("Remove", "Eliminar"), func() { u.mcpClearToken(s.ID) }))
		} else {
			actions = u.uxsActions(u.button("mcp.save."+s.ID, u.tr("Save credential", "Guardar credencial"), func() { u.mcpSaveToken(s) }))
		}
		rows = append(rows, u.uxsRow(u.tr("Credential", "Credencial"), u.tr("Stored locally; never shown again.", "Guardada localmente; nunca se vuelve a mostrar."), credential, actions, nil))
	}
	if s.Auth == "token" && len(s.Modes) > 1 {
		choices := []nativeChoice{{Value: "A", Label: u.mcpModeLabel("A")}, {Value: "B", Label: u.mcpModeLabel("B")}}
		rows = append(rows, u.uxsRow(u.tr("Delivery", "Entrega"), "", u.uxsRadioList("mcp.mode."+s.ID+".", choices, s.Mode, s.Enabled, func(mode string) {
			u.mcpSetState(s.ID, map[string]any{"mode": mode})
		}), nil, nil))
	}
	if s.Auth == "oauth" {
		var actions layout.Widget
		switch {
		case u.mcpState().oauth == s.ID:
			actions = u.uxsActions(u.button("mcp.oauth.cancel."+s.ID, u.tr("Cancel sign-in", "Cancelar inicio de sesión"), u.mcpCancelSignIn))
		case s.Connected:
			actions = u.uxsActions(u.button("mcp.clear."+s.ID, u.tr("Remove", "Eliminar"), func() { u.mcpClearToken(s.ID) }))
		default:
			actions = u.uxsActions(u.button("mcp.oauth."+s.ID, u.tr("Sign in", "Iniciar sesión"), func() { u.mcpSignIn(s.ID) }))
		}
		rows = append(rows, u.uxsRow(u.tr("Sign-in", "Inicio de sesión"), u.tr("Access tokens expire, so Kilo refreshes one at launch and never writes it to a file.", "Los tokens de acceso caducan, así que Kilo renueva uno al abrir y nunca lo escribe en un archivo."), nil, actions, nil))
	}
	if s.DocsURL != "" {
		url := s.DocsURL
		rows = append(rows, u.uxsRow(u.tr("Documentation", "Documentación"), "", nil, u.uxsActions(u.button("mcp.docs."+s.ID, u.tr("Open documentation", "Abrir documentación"), func() { u.open(url) })), nil))
	}
	return u.uxsBlock(u.mcpName(s), u.mcpAuthLabel(s.Auth), rows...)
}

func (u *nativeUI) mcpSyncPanel() layout.Widget {
	s := u.mcpState()
	names := make([]string, 0, len(s.harnesses))
	for _, harness := range s.harnesses {
		names = append(names, harness.Name)
	}
	rows := []layout.Widget{
		u.uxsRow(u.tr("Apps", "Apps"), strings.Join(names, " · "), nil, u.uxsActions(u.button("mcp.sync", u.tr("Synchronize", "Sincronizar"), u.mcpSync)), nil),
	}
	if s.synced {
		for _, report := range s.results {
			rows = append(rows, u.mcpResultRow(report))
		}
	}
	return u.uxsBlock(u.tr("Synchronize every app", "Sincronizar todas las apps"), u.tr("Writes each app's own MCP configuration", "Escribe la configuración MCP de cada app"), rows...)
}

func (u *nativeUI) mcpReasonLabel(report mcpSyncReport) string {
	if label := map[string]string{
		"profile-not-prepared": u.tr("This app has not been prepared yet", "Esta app aún no se ha preparado"),
		"unreadable-config":    u.tr("Its configuration cannot be read safely", "Su configuración no se puede leer de forma segura"),
	}[report.ReasonCode]; label != "" {
		return label
	}
	if report.Reason == "" {
		return ""
	}
	return nativeMessage(report.Reason, u.language)
}

func (u *nativeUI) mcpProblemLabel(problem mcpProblem) string {
	name := mcpHarnessName(problem.Server)
	switch problem.Code {
	case "missing-token":
		return name + ": " + u.tr("no saved credential", "sin credencial guardada")
	case "no-launch-variable":
		return name + ": " + u.tr("no launch variable", "sin variable de arranque")
	case "unsupported-auth":
		return name + ": " + u.tr("unsupported credential kind", "tipo de credencial no admitido")
	}
	return name + ": " + nativeMessage(problem.Detail, u.language)
}

func (u *nativeUI) mcpResultRow(report mcpSyncReport) layout.Widget {
	status := map[string]string{
		"changed":   u.tr("Updated", "Actualizado"),
		"unchanged": u.tr("Already up to date", "Ya está al día"),
		"skipped":   u.tr("Skipped", "Omitido"),
		"failed":    u.tr("Failed", "Falló"),
	}[report.Status]
	detail := u.mcpReasonLabel(report)
	problems := make([]string, 0, len(report.Problems))
	for _, problem := range report.Problems {
		problems = append(problems, u.mcpProblemLabel(problem))
	}
	if len(report.Skipped) > 0 {
		problems = append(problems, u.tr("In use by another configuration: ", "En uso por otra configuración: ")+strings.Join(report.Skipped, ", "))
	}
	if detail == "" && len(problems) > 0 {
		detail = strings.Join(problems, " · ")
	}
	return u.uxsRow(mcpHarnessName(report.Harness), detail, u.textStyle(14, status, nativeText, font.Medium), nil, nil)
}

// mcpAcceptState reacts to the MCP sign-in summary carried by /api/state.
func (u *nativeUI) mcpAcceptState() {
	s := u.mcpState()
	flow := nativeMap(u.state["mcpOAuth"])
	server, status := nativeString(flow, "server"), nativeString(flow, "status")
	mark := server + ":" + status
	if mark == s.oauthSeen {
		return
	}
	s.oauthSeen = mark
	if status == "starting" || status == "pending" {
		s.oauth = server
		return
	}
	s.oauth = ""
	switch status {
	case "connected":
		u.setNotice(nativeToneSuccess, u.tr("Sign-in complete.", "Inicio de sesión completado."))
		u.mcpLoad()
	case "error":
		u.setNotice(nativeToneError, nativeMessage(nativeString(flow, "message"), u.language))
		u.mcpLoad()
	case "cancelled":
		u.mcpLoad()
	}
}

func (u *nativeUI) mcpPanel() layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		s := u.mcpState()
		if !s.started {
			u.mcpLoad()
		}
		var widgets []layout.Widget
		if s.warning != "" {
			widgets = append(widgets, mcpPad(u.message(nativeToneWarning, u.tr("Saved MCP settings need review. The existing files have not been changed.", "Los ajustes MCP guardados necesitan revisión. Los archivos existentes no se han cambiado."))))
		}
		if s.error != "" {
			widgets = append(widgets, mcpPad(u.message(nativeToneError, s.error)))
			if !s.loaded {
				widgets = append(widgets, mcpPad(u.uxsActions(u.button("mcp.retry", u.tr("Retry", "Reintentar"), u.mcpLoad))))
			}
		}
		widgets = append(widgets, mcpPad(u.modelSearchField("mcp.search", u.tr("Search servers", "Buscar servidores"))))
		if !s.loaded {
			widgets = append(widgets, mcpPad(u.note(u.tr("Loading the MCP catalog…", "Cargando el catálogo MCP…"))))
		}
		visible := u.mcpVisibleServers()
		if s.loaded && len(visible) == 0 {
			widgets = append(widgets, mcpPad(u.note(u.tr("No MCP servers match your search.", "Ningún servidor MCP coincide con tu búsqueda."))))
		}
		for _, server := range visible {
			widgets = append(widgets, mcpPad(u.mcpServerCard(server)))
		}
		if s.loaded {
			widgets = append(widgets, mcpPad(u.mcpSyncPanel()))
		}
		widgets = append(widgets, u.note(u.tr("Credentials live in your local credential vault and are never shown again. Servers injected at launch keep the secret out of every configuration file.", "Las credenciales viven en tu almacén local y nunca se vuelven a mostrar. Los servidores inyectados al abrir mantienen el secreto fuera de todos los archivos de configuración.")))
		return u.column(widgets...)(gtx)
	}
}

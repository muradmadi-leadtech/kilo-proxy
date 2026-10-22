export function mcpAuthLabel(auth, language = 'en') {
 const labels = {token:['Token','Token'],none:['No credential','Sin credencial'],oauth:['OAuth (coming soon)','OAuth (próximamente)']};
 return (labels[auth] || ['',''])[language === 'es' ? 1 : 0];
}

export function mcpModeLabel(mode, language = 'en') {
 const labels = {A:['Literal value in config','Valor literal en la configuración'],B:['Injected at launch (no secret stored)','Inyectado al abrir (sin guardar secreto)']};
 return (labels[mode] || ['',''])[language === 'es' ? 1 : 0];
}

// A disabled server is off; OAuth has no working writer yet; a token server is
// ready only once the vault holds a token; an open server needs no credential.
export function mcpServerStatus(server) {
 if (!server?.enabled) return {ready:false, key:'off'};
 if (server.auth === 'oauth') return {ready:false, key:'oauth-pending'};
 if (server.auth === 'none') return {ready:true, key:'ready'};
 if (server.auth === 'token') return server.hasToken ? {ready:true, key:'ready'} : {ready:false, key:'needs-token'};
 return {ready:false, key:'off'};
}

// Writes the curated MCP catalog into the harness profiles Kilo prepares. The
// token is read from the password field only on the explicit Save click, is
// posted straight to the vault endpoint and is never kept in render state.
export function createMCPHelper({api, onChange}) {
 const $ = id => document.getElementById(id);
 let ctx = {state:null, language:'en'};
 let servers = [], harnesses = [], warning = '', results = null;
 let loaded = false, loading = false, working = false, error = '', wired = false, pending = null, revision = 0;
 const L = (en, es) => ctx.language === 'es' ? es : en;
 const failed = failure => failure?.message || String(failure);
 const statusLabel = key => key === 'ready' ? L('Ready','Listo') : key === 'needs-token' ? L('Add a token','Añade un token') : key === 'oauth-pending' ? L('OAuth pending','OAuth pendiente') : L('Not enabled','Desactivado');
 const localized = text => text?.[ctx.language === 'es' ? 'es' : 'en'] || '';
 const query = () => { const field = $('mcp-search'); return field && typeof field.value === 'string' ? field.value.trim().toLowerCase() : ''; };
 function matches(server, needle) {
  return [server.id, server.name?.en, server.name?.es, server.description?.en, server.description?.es].filter(Boolean).join(' ').toLowerCase().includes(needle);
 }
 const filtered = () => { const needle = query(); return needle ? servers.filter(server => matches(server, needle)) : servers.slice(); };
 function card(server) {
  const item = document.createElement('article');
  item.className = 'mcp-card';
  const name = document.createElement('h3');
  name.textContent = localized(server.name) || server.id;
  const description = document.createElement('p');
  description.textContent = localized(server.description);
  const meta = document.createElement('p');
  meta.className = 'mcp-card-meta';
  const mode = server.auth === 'token' && Array.isArray(server.modes) && server.modes.length ? (server.mode || 'A') : '';
  meta.textContent = [mcpAuthLabel(server.auth, ctx.language), mode ? mcpModeLabel(mode, ctx.language) : '', statusLabel(mcpServerStatus(server).key)].filter(Boolean).join(' · ');
  const enable = document.createElement('label');
  enable.className = 'checkbox-label';
  const toggleInput = document.createElement('input');
  toggleInput.type = 'checkbox'; toggleInput.checked = !!server.enabled; toggleInput.disabled = working;
  toggleInput.addEventListener('change', () => { void toggle(server.id, toggleInput.checked); });
  const enableText = document.createElement('span');
  enableText.textContent = L('Enabled','Activado');
  enable.append(toggleInput, enableText);
  item.append(name, description, meta, enable);
  if (mode) {
   const select = document.createElement('select');
   select.value = mode;
   for (const value of server.modes) { const option = document.createElement('option'); option.value = value; option.textContent = value + ' · ' + mcpModeLabel(value, ctx.language); select.append(option); }
   select.addEventListener('change', () => { void setMode(server.id, select.value); });
   item.append(select);
  }
  if (server.auth === 'token') {
   const field = document.createElement('input');
   field.type = 'password'; field.autocomplete = 'off'; field.placeholder = L('Paste the token (never shown again)','Pega el token (no se vuelve a mostrar)');
   const save = document.createElement('button');
   save.type = 'button'; save.textContent = L('Save token','Guardar token'); save.disabled = working;
   save.addEventListener('click', () => { void saveToken(server.id, field.value); });
   const clear = document.createElement('button');
   clear.type = 'button'; clear.textContent = L('Clear token','Borrar token'); clear.disabled = working || !server.hasToken;
   clear.addEventListener('click', () => { void clearToken(server.id); });
   item.append(field, save, clear);
  }
  if (server.docsUrl) {
   const link = document.createElement('a');
   link.href = server.docsUrl; link.target = '_blank'; link.rel = 'noopener noreferrer';
   link.textContent = L('Documentation ↗','Documentación ↗');
   item.append(link);
  }
  return item;
 }
 function render(next = ctx) {
  ctx = next;
  if (!wired) { wired = true; $('mcp-sync')?.addEventListener('click', () => void sync()); $('mcp-search')?.addEventListener('input', () => render()); }
  $('mcp-intro').textContent = L('Connect curated MCP servers to the harnesses Kilo prepares. Kilo writes each harness profile and keeps your own server entries untouched.','Conecta servidores MCP seleccionados a los harness que prepara Kilo. Kilo escribe el perfil de cada harness y conserva tus propias entradas.');
  $('mcp-search').placeholder = L('Search servers','Buscar servidores');
  const harnessList = $('mcp-harnesses');
  harnessList.replaceChildren();
  for (const harness of harnesses) { const item = document.createElement('li'); item.textContent = harness.name || harness.id; harnessList.append(item); }
  $('mcp-warning').textContent = warning;
  $('mcp-warning').hidden = !warning;
  $('mcp-sync').textContent = working ? L('Writing configuration…','Escribiendo la configuración…') : L('Write harness configuration','Escribir la configuración de los harness');
  $('mcp-sync').disabled = working || loading;
  const list = $('mcp-list');
  list.replaceChildren();
  for (const server of filtered()) list.append(card(server));
  $('mcp-status').textContent = error || (working ? L('Applying the change…','Aplicando el cambio…') : loading ? L('Loading the MCP catalog…','Cargando el catálogo MCP…') : loaded ? L('MCP catalog loaded.','Catálogo MCP cargado.') : '');
  $('mcp-status').classList.toggle('error', !!error);
  $('mcp-privacy').textContent = L('The credential is stored in the local credential vault and never shown again. Mode B keeps it out of every config file.','La credencial se guarda en el almacén local de credenciales y no se vuelve a mostrar. El modo B la mantiene fuera de todos los archivos de configuración.');
  onChange();
  if (!loaded && !loading) void reload();
 }
 function accept(next) {
  servers = Array.isArray(next?.servers) ? next.servers : [];
  harnesses = Array.isArray(next?.harnesses) ? next.harnesses : [];
  warning = typeof next?.warning === 'string' ? next.warning : '';
 }
 async function load() {
  loading = true; error = ''; const generation = ++revision; render();
  try { const next = await api('mcp/catalog'); if (generation === revision) accept(next); }
  catch (failure) { if (generation === revision) error = failed(failure); }
  finally { if (generation === revision) { loaded = true; loading = false; } render(); }
 }
 function reload() {
  if (!pending) pending = load().finally(() => { pending = null; });
  return pending;
 }
 // A mutation keeps the catalog it already has when the write fails: the
 // error stays visible instead of being cleared by a successful reload.
 async function commit(run) {
  working = true; error = ''; render();
  let ok = true;
  try { await run(); }
  catch (failure) { error = failed(failure); ok = false; }
  finally { working = false; render(); }
  if (!ok) return false;
  await reload();
  return true;
 }
 async function sync() {
  if (working) return results;
  results = null;
  const ok = await commit(async () => { const reply = await api('mcp/sync', {}); results = Array.isArray(reply?.results) ? reply.results : []; });
  return ok ? results : null;
 }
 function toggle(id, enabled) {
  return working ? false : commit(() => api('mcp/state', {id, enabled: !!enabled}));
 }
 function setMode(id, mode) {
  return working ? false : commit(() => api('mcp/state', {id, mode}));
 }
 async function saveToken(id, token) {
  const value = typeof token === 'string' ? token.trim() : '';
  if (!value || working) return false;
  return commit(() => api('mcp/auth', {id, token: value}));
 }
 function clearToken(id) {
  return working ? false : commit(() => api('mcp/auth', {id}, 'DELETE'));
 }
 return {render, reload, sync, toggle, setMode, saveToken, clearToken, state:() => ({loaded, loading, working, error, servers, harnesses, warning, results, filtered:filtered()})};
}

import test from 'node:test';
import assert from 'node:assert/strict';
import {createMCPHelper, mcpAuthLabel, mcpModeLabel, mcpServerStatus} from '../ui/mcp-helper.mjs';

const server = (over = {}) => ({id:'supermetrics', name:{en:'Supermetrics', es:'Supermetrics'}, description:{en:'Marketing data', es:'Datos de marketing'}, url:'https://mcp.example/mcp', auth:'token', modes:['A','B'], envVar:'KILO_MCP_SUPERMETRICS_TOKEN', enabled:false, mode:'', hasToken:false, ...over});
const catalog = () => ({servers:[server({id:'supermetrics', name:{en:'Supermetrics', es:'Supermetrics'}, description:{en:'Marketing data from 100+ sources', es:'Datos de marketing'}}), server({id:'appsflyer', name:{en:'AppsFlyer', es:'AppsFlyer'}, description:{en:'Mobile attribution', es:'Atribución móvil'}, docsUrl:'https://appsflyer.example/docs'})], harnesses:[{id:'codex', name:'Codex Desktop'}, {id:'codex-cli', name:'Codex CLI'}, {id:'claude', name:'Claude Code'}], warning:''});
const tick = () => new Promise(resolve => setImmediate(resolve));
function fixture(t, api) {
 const previous = globalThis.document, nodes = new Map(), calls = [];
 const element = () => {
  const node = {children:[],textContent:'',checked:false,disabled:false,hidden:false,placeholder:'',className:'',type:'',href:'',classList:{toggle(){}},replaceChildren(){this.children=[];},addEventListener(){}};
  let value = '';
  // A real <select> only holds a value it has a matching <option> for, and
  // adopts its first option once one exists. Modelling that is what makes the
  // "set value before appending options" bug observable.
  Object.defineProperty(node, 'value', {
   get(){ return value; },
   set(next){ value = node.tagName === 'select' && !node.children.some(child => child.tagName === 'option' && child.value === next) ? '' : next; },
  });
  node.append = (...items) => {
   node.children.push(...items);
   if (node.tagName === 'select' && value === '') {
    const first = node.children.find(child => child.tagName === 'option');
    if (first) value = first.value;
   }
  };
  return node;
 };
 globalThis.document = {getElementById(id){if(!nodes.has(id))nodes.set(id,element());return nodes.get(id);},createElement(tag){const node = element(); node.tagName = tag === 'select' ? 'select' : tag === 'option' ? 'option' : tag; return node;}};
 t.after(()=>{globalThis.document = previous;});
 const scheduled = [];
 const helper = createMCPHelper({api:(path, body, method) => {calls.push({path, body, method}); return api(path, body, method);}, onChange(){}, schedule:(run, ms)=>{scheduled.push({run, ms});}});
 helper.render({state:{}, language:'en'});
 return {helper, nodes, calls, scheduled};
}

test('mcpServerStatus covers ready, needs-token, needs-signin and off', () => {
 assert.deepEqual(mcpServerStatus(server({enabled:true, hasToken:true})), {ready:true, key:'ready'});
 assert.deepEqual(mcpServerStatus(server({enabled:true, auth:'none', hasToken:false})), {ready:true, key:'ready'});
 assert.deepEqual(mcpServerStatus(server({enabled:true, hasToken:false})), {ready:false, key:'needs-token'});
 assert.deepEqual(mcpServerStatus(server({enabled:true, auth:'oauth', connected:false})), {ready:false, key:'needs-signin'});
 assert.deepEqual(mcpServerStatus(server({enabled:true, auth:'oauth', connected:true})), {ready:true, key:'ready'});
 assert.deepEqual(mcpServerStatus(server({enabled:false, hasToken:true})), {ready:false, key:'off'});
 assert.deepEqual(mcpServerStatus(server({enabled:false, auth:'oauth'})), {ready:false, key:'off'});
 assert.deepEqual(mcpServerStatus(undefined), {ready:false, key:'off'});
});

test('mcpAuthLabel and mcpModeLabel are bilingual with English as default', () => {
 assert.equal(mcpAuthLabel('token', 'en'), 'Token');
 assert.equal(mcpAuthLabel('token', 'es'), 'Token');
 assert.equal(mcpAuthLabel('none'), 'No credential');
 assert.equal(mcpAuthLabel('none', 'es'), 'Sin credencial');
 assert.equal(mcpAuthLabel('oauth', 'en'), 'OAuth');
 assert.equal(mcpAuthLabel('oauth', 'es'), 'OAuth');
 assert.equal(mcpAuthLabel('unknown', 'es'), '');
 assert.equal(mcpModeLabel('A', 'en'), 'Literal value in config');
 assert.equal(mcpModeLabel('A', 'es'), 'Valor literal en la configuración');
 assert.equal(mcpModeLabel('B'), 'Injected at launch (no secret stored)');
 assert.equal(mcpModeLabel('B', 'es'), 'Inyectado al abrir (sin guardar secreto)');
 assert.equal(mcpModeLabel('', 'en'), '');
 assert.equal(mcpModeLabel(undefined, 'es'), '');
});

test('reload renders one card per catalog server and keeps the harness list', async t => {
 const {helper, nodes, calls} = fixture(t, async path => path === 'mcp/catalog' ? catalog() : {});
 await helper.reload();
 assert.equal(helper.state().servers.length, 2);
 assert.equal(helper.state().warning, '');
 assert.deepEqual(helper.state().harnesses.map(h => h.id), ['codex','codex-cli','claude']);
 assert.equal(nodes.get('mcp-list').children.length, 2);
 assert.deepEqual(nodes.get('mcp-harnesses').children.map(node => node.textContent), ['Codex Desktop','Codex CLI','Claude Code']);
 assert.equal(nodes.get('mcp-warning').hidden, true);
 assert.match(nodes.get('mcp-privacy').textContent, /local credential vault/);
 assert.ok(calls.some(call => call.path === 'mcp/catalog'));
});

test('search filters servers case-insensitively by id, name and description', async t => {
 const {helper, nodes} = fixture(t, async () => catalog());
 await helper.reload();
 nodes.get('mcp-search').value = 'MARKETING';
 assert.deepEqual(helper.state().filtered.map(s => s.id), ['supermetrics']);
 helper.render();
 assert.equal(nodes.get('mcp-list').children.length, 1);
 nodes.get('mcp-search').value = 'appsflyer';
 assert.deepEqual(helper.state().filtered.map(s => s.id), ['appsflyer']);
 nodes.get('mcp-search').value = 'MÓVIL';
 assert.deepEqual(helper.state().filtered.map(s => s.id), ['appsflyer']);
 nodes.get('mcp-search').value = '';
 assert.equal(helper.state().filtered.length, 2);
 helper.render({state:{}, language:'es'});
 assert.match(nodes.get('mcp-privacy').textContent, /almacén local de credenciales/);
});

test('saveToken refuses an empty or blank token and never posts to mcp/auth', async t => {
 const {helper, calls} = fixture(t, async path => path === 'mcp/catalog' ? catalog() : {ok:true});
 await helper.reload();
 assert.equal(await helper.saveToken('supermetrics', ''), false);
 assert.equal(await helper.saveToken('supermetrics', '   '), false);
 assert.equal(calls.filter(call => call.path === 'mcp/auth').length, 0);
});

test('sync posts an empty body, reloads and surfaces the per-harness results', async t => {
 const results = [{harness:'codex', status:'changed', backup:'config.toml.bak'}, {harness:'claude', status:'skipped', reason:'profile not prepared'}, {harness:'codex-cli', status:'failed', problems:['supermetrics: no saved token']}];
 const {helper, calls, nodes} = fixture(t, async path => path === 'mcp/catalog' ? catalog() : {ok:true, results});
 await helper.reload();
 const returned = await helper.sync();
 assert.deepEqual(returned, results);
 assert.deepEqual(helper.state().results, results);
 const sent = calls.find(call => call.path === 'mcp/sync');
 assert.deepEqual(sent.body, {});
 assert.ok(calls.filter(call => call.path === 'mcp/catalog').length >= 2);
 assert.equal(nodes.get('mcp-status').textContent, 'MCP catalog loaded.');
});

test('toggle, setMode and clearToken post the right payloads and reload', async t => {
 const {helper, calls} = fixture(t, async path => path === 'mcp/catalog' ? catalog() : {ok:true});
 await helper.reload();
 await helper.toggle('supermetrics', true);
 await helper.setMode('supermetrics', 'B');
 await helper.clearToken('supermetrics');
 assert.deepEqual(calls.filter(call => call.path === 'mcp/state').map(call => call.body), [{id:'supermetrics', enabled:true}, {id:'supermetrics', mode:'B'}]);
 const cleared = calls.find(call => call.path === 'mcp/auth');
 assert.deepEqual(cleared.body, {id:'supermetrics'});
 assert.equal(cleared.method, 'DELETE');
});

test('a failing api surfaces the error in state and never throws out of the helper', async t => {
 const {helper, nodes} = fixture(t, async () => { throw new Error('The MCP catalog is unavailable.'); });
 await tick();
 assert.equal(helper.state().loaded, true);
 assert.match(helper.state().error, /MCP catalog is unavailable/);
 assert.match(nodes.get('mcp-status').textContent, /MCP catalog is unavailable/);
 const failing = fixture(t, async path => { if (path === 'mcp/catalog') return catalog(); throw new Error('The configuration could not be written.'); });
 await failing.helper.reload();
 await failing.helper.sync();
 assert.match(failing.helper.state().error, /could not be written/);
 assert.equal(failing.helper.state().results, null);
});

test('a saved token is never stored in state or written into status text', async t => {
 const token = 'kilo_mcp_secret_token_4242';
 const {helper, nodes, calls} = fixture(t, async path => path === 'mcp/auth' ? {ok:true} : catalog());
 await helper.reload();
 assert.equal(await helper.saveToken('supermetrics', token), true);
 const auth = calls.find(call => call.path === 'mcp/auth');
 assert.deepEqual(auth.body, {id:'supermetrics', token});
 assert.equal(JSON.stringify(helper.state()).includes(token), false);
 assert.equal(nodes.get('mcp-status').textContent.includes(token), false);
 for (const node of nodes.values()) assert.equal(node.textContent.includes(token), false);
 assert.equal(nodes.get('mcp-list').children.length, 2);
});

test('oauthStart posts mcp/oauth/start, exposes the sign-in URL and schedules one poll', async t => {
 const {helper, calls, scheduled} = fixture(t, async path => {
  if (path === 'mcp/catalog') return catalog();
  if (path === 'mcp/oauth/status') return {flow:{server:'amplitude', status:'pending'}};
  return {status:'pending', authorizationUrl:'https://as.example/authorize?state=abc'};
 });
 await helper.reload();
 assert.equal(await helper.oauthStart('amplitude'), true);
 const started = calls.find(call => call.path === 'mcp/oauth/start');
 assert.deepEqual(started.body, {id:'amplitude'});
 assert.equal(helper.state().oauth.url, 'https://as.example/authorize?state=abc');
 assert.equal(helper.state().oauth.server, 'amplitude');
 assert.equal(scheduled.length, 1);
 assert.equal(scheduled[0].ms, 1200);
});

test('oauthCancel clears the flow and posts mcp/oauth/cancel', async t => {
 const {helper, calls} = fixture(t, async path => path === 'mcp/catalog' ? catalog() : {ok:true});
 await helper.reload();
 await helper.oauthStart('amplitude');
 await helper.oauthCancel();
 const cancelled = calls.find(call => call.path === 'mcp/oauth/cancel');
 assert.deepEqual(cancelled.body, {});
 assert.equal(helper.state().oauth.server, '');
});

test('pollOAuth keeps polling while the flow runs and settles when it connects', async t => {
 const running = fixture(t, async path => {
  if (path === 'mcp/catalog') return catalog();
  if (path === 'mcp/oauth/start') return {status:'pending', authorizationUrl:'https://as.example/a'};
  if (path === 'mcp/oauth/status') return {flow:{server:'amplitude', status:'pending'}};
  return {ok:true};
 });
 await running.helper.reload();
 await running.helper.oauthStart('amplitude');
 await running.helper.pollOAuth();
 assert.equal(running.helper.state().oauth.server, 'amplitude');
 assert.ok(running.scheduled.length >= 2, 'a running flow must schedule the next poll');

 const settled = fixture(t, async path => {
  if (path === 'mcp/catalog') return catalog();
  if (path === 'mcp/oauth/start') return {status:'pending', authorizationUrl:'https://as.example/a'};
  if (path === 'mcp/oauth/status') return {flow:{server:'amplitude', status:'connected'}};
  return {ok:true};
 });
 await settled.helper.reload();
 await settled.helper.oauthStart('amplitude');
 await settled.helper.pollOAuth();
 assert.equal(settled.helper.state().oauth.server, '');
});

test('a failed sign-in start reports the error and keeps something actionable', async t => {
 const {helper} = fixture(t, async path => { if (path === 'mcp/catalog') return catalog(); throw new Error('An MCP sign-in is already in progress.'); });
 await helper.reload();
 assert.equal(await helper.oauthStart('amplitude'), false);
 assert.match(helper.state().error, /already in progress/);
});

test('a mode selector reflects the saved mode, not the first option', async t => {
 // Regression: setting a select's value before its options exist leaves it on
 // the first option, so a card kept showing "A" while the state said "B".
 for (const mode of ['A', 'B']) {
  const withMode = {servers:[server({id:'supermetrics', enabled:true, mode})], harnesses:[], warning:''};
  const {helper, nodes} = fixture(t, async () => withMode);
  await helper.reload();
  const select = nodes.get('mcp-list').children[0].children.find(child => child.tagName === 'select');
  assert.ok(select, 'a token server with two modes needs a selector');
  assert.equal(select.value, mode, `the selector must show the saved mode ${mode}`);
 }
});

test('a card for a saved mode B renders the selector on B', async t => {
 const withB = {servers:[server({id:'supermetrics', enabled:true, mode:'B'})], harnesses:[], warning:''};
 const {helper, nodes} = fixture(t, async () => withB);
 await helper.reload();
 const select = nodes.get('mcp-list').children[0].children.find(child => child.tagName === 'select');
 assert.ok(select, 'a token server with two modes needs a selector');
 assert.equal(select.value, 'B');
});

test('sync renders one line per harness and hides the report until it runs', async t => {
 const results = [
  {harness:'codex', status:'changed'},
  {harness:'codex-cli', status:'skipped', reasonCode:'profile-not-prepared'},
  {harness:'claude', status:'failed', problems:[{server:'supermetrics', code:'missing-token'}], skipped:['appsflyer']},
 ];
 const {helper, nodes} = fixture(t, async path => path === 'mcp/catalog' ? catalog() : {ok:true, results});
 await helper.reload();
 assert.equal(nodes.get('mcp-report').hidden, true, 'no report before a sync');
 await helper.sync();
 const report = nodes.get('mcp-report');
 assert.equal(report.hidden, false);
 assert.equal(report.children.length, 3);
 assert.match(report.children[0].textContent, /Codex Desktop · Updated/);
 assert.match(report.children[1].textContent, /Codex CLI · Skipped · This app has not been prepared yet/);
 assert.match(report.children[2].textContent, /Claude Code · Failed/);
 assert.match(report.children[2].textContent, /supermetrics: no saved credential/);
 assert.match(report.children[2].textContent, /In use by another configuration: appsflyer/);
});

test('the sync report is localized in Spanish, with no backend prose leaking', async t => {
 const results = [
  {harness:'codex', status:'skipped', reasonCode:'profile-not-prepared'},
  {harness:'claude', status:'failed', problems:[{server:'supermetrics', code:'missing-token'}]},
 ];
 const {helper, nodes} = fixture(t, async path => path === 'mcp/catalog' ? catalog() : {ok:true, results});
 await helper.reload();
 helper.render({state:{}, language:'es'});
 await helper.sync();
 const text = nodes.get('mcp-report').children.map(node => node.textContent).join(' | ');
 assert.match(text, /Omitido · Esta app aún no se ha preparado/);
 assert.match(text, /Falló · supermetrics: sin credencial guardada/);
 assert.equal(/profile-not-prepared|missing-token|no saved credential/.test(text), false, 'codes and English must not reach the user');
});

test('an unknown reason falls back to the phrase i18n knows, never to a code', async t => {
 const {helper, nodes} = fixture(t, async path => path === 'mcp/catalog' ? catalog() : {ok:true, results:[{harness:'codex', status:'failed', reason:'Método no permitido.', reasonCode:'something-new'}]});
 await helper.reload();
 await helper.sync();
 assert.equal(nodes.get('mcp-report').children[0].textContent.includes('something-new'), false);
 assert.match(nodes.get('mcp-report').children[0].textContent, /Method not allowed/);
});

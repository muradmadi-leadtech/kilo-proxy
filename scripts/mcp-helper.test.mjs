import test from 'node:test';
import assert from 'node:assert/strict';
import {createMCPHelper, mcpAuthLabel, mcpModeLabel, mcpServerStatus} from '../ui/mcp-helper.mjs';

const server = (over = {}) => ({id:'supermetrics', name:{en:'Supermetrics', es:'Supermetrics'}, description:{en:'Marketing data', es:'Datos de marketing'}, url:'https://mcp.example/mcp', auth:'token', modes:['A','B'], envVar:'KILO_MCP_SUPERMETRICS_TOKEN', enabled:false, mode:'', hasToken:false, ...over});
const catalog = () => ({servers:[server({id:'supermetrics', name:{en:'Supermetrics', es:'Supermetrics'}, description:{en:'Marketing data from 100+ sources', es:'Datos de marketing'}}), server({id:'appsflyer', name:{en:'AppsFlyer', es:'AppsFlyer'}, description:{en:'Mobile attribution', es:'Atribución móvil'}, docsUrl:'https://appsflyer.example/docs'})], harnesses:[{id:'codex', name:'Codex Desktop'}, {id:'codex-cli', name:'Codex CLI'}, {id:'claude', name:'Claude Code'}], warning:''});
const tick = () => new Promise(resolve => setImmediate(resolve));
function fixture(t, api) {
 const previous = globalThis.document, nodes = new Map(), calls = [];
 const element = () => ({children:[],textContent:'',value:'',checked:false,disabled:false,hidden:false,placeholder:'',className:'',type:'',href:'',classList:{toggle(){}},replaceChildren(){this.children=[];},append(...items){this.children.push(...items);},addEventListener(){}});
 globalThis.document = {getElementById(id){if(!nodes.has(id))nodes.set(id,element());return nodes.get(id);},createElement:element};
 t.after(()=>{globalThis.document = previous;});
 const helper = createMCPHelper({api:(path, body, method) => {calls.push({path, body, method}); return api(path, body, method);}, onChange(){}});
 helper.render({state:{}, language:'en'});
 return {helper, nodes, calls};
}

test('mcpServerStatus covers ready, needs-token, oauth-pending and off', () => {
 assert.deepEqual(mcpServerStatus(server({enabled:true, hasToken:true})), {ready:true, key:'ready'});
 assert.deepEqual(mcpServerStatus(server({enabled:true, auth:'none', hasToken:false})), {ready:true, key:'ready'});
 assert.deepEqual(mcpServerStatus(server({enabled:true, hasToken:false})), {ready:false, key:'needs-token'});
 assert.deepEqual(mcpServerStatus(server({enabled:true, auth:'oauth', hasToken:false})), {ready:false, key:'oauth-pending'});
 assert.deepEqual(mcpServerStatus(server({enabled:false, hasToken:true})), {ready:false, key:'off'});
 assert.deepEqual(mcpServerStatus(server({enabled:false, auth:'oauth'})), {ready:false, key:'off'});
 assert.deepEqual(mcpServerStatus(undefined), {ready:false, key:'off'});
});

test('mcpAuthLabel and mcpModeLabel are bilingual with English as default', () => {
 assert.equal(mcpAuthLabel('token', 'en'), 'Token');
 assert.equal(mcpAuthLabel('token', 'es'), 'Token');
 assert.equal(mcpAuthLabel('none'), 'No credential');
 assert.equal(mcpAuthLabel('none', 'es'), 'Sin credencial');
 assert.equal(mcpAuthLabel('oauth', 'en'), 'OAuth (coming soon)');
 assert.equal(mcpAuthLabel('oauth', 'es'), 'OAuth (próximamente)');
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

// Optional real-IDE smoke test: CODE_SERVER_BINARY=/path/to/code-server node e2e/vscode-check.mjs
import assert from 'node:assert/strict';
import { writeFileSync, readFileSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { start, setup, killTmux, totals } from './harness.mjs';
const binary = process.env.CODE_SERVER_BINARY;
if (!binary) throw new Error('Set CODE_SERVER_BINARY to an installed code-server');
const s = await start({ viewport: { width: 1440, height: 1000 } });
try {
 await setup(s.page,s.url);
 const settings = await (await s.context.request.get(s.url+'/api/settings')).json();
 settings.settings.harnesses.vscode.binary = binary;
 settings.settings.workspace.allow_custom = true;
 await s.context.request.put(s.url+'/api/settings',{data:settings});
 let catalogue;
 // The first probe of a cold code-server can outlast the catalogue's timeout.
 for (let attempt = 0; attempt < 3; attempt += 1) {
  catalogue = await (await s.context.request.post(s.url+'/api/harnesses/refresh')).json();
  if (catalogue.harnesses.find(h=>h.id==='vscode').installed) break;
 }
 assert(catalogue.harnesses.find(h=>h.id==='vscode').installed);
 const project = join(s.data,'project');
 mkdirSync(project,{recursive:true});
 const refused = await s.context.request.post(s.url+'/api/sessions',{data:{harness:'vscode',workdir_mode:'custom',workdir:project}});
 assert.equal(refused.status(),400);
 const shell=await (await s.context.request.post(s.url+'/api/sessions',{data:{harness:'shell',workdir_mode:'custom',workdir:project}})).json();
 await s.page.goto(s.url+'/#'+shell.session.id);
 await s.page.reload();
 await s.page.locator('#newSession').click();
 assert.equal(await s.page.locator('#nsTools [data-value="vscode"]').count(),0);
 await s.page.locator('#nsCancel').click();
 console.log('VS Code is not on the new-session sheet');
 writeFileSync(join(project,'smoke.txt'),'hello from Socrates\n');
 // The folder's VS Code is in its ⋮ menu in the sidebar.
 const ideButton = { click: async () => {
  await s.page.locator('.so-folder[data-folder="'+project+'"] > .so-folder__bar > .so-folder__more').click();
  await s.page.locator('#sessionMenu:not([hidden]) [data-action="ide"]').click();
 } };
 // VS Code always opens in a tab of its own, never in the pane.
 const openTab = async (trigger, timeout = 45000) => {
  const [tab] = await Promise.all([s.context.waitForEvent('page'), trigger()]);
  await tab.locator('.monaco-workbench').waitFor({timeout});
  assert.equal(await s.page.locator('tab.so-ide__frame').count(),0);
  return tab;
 };
 let tab = await openTab(() => ideButton.click());
 assert.match(tab.url(), /\/ide\/[^/]+\/\?folder=/);
 const list = (await (await s.context.request.get(s.url+'/api/sessions')).json()).sessions;
 const row = list.find((one)=>one.harness==='vscode');
 assert(row && row.workdir===project);
 assert.equal(await s.page.locator('.so-row[data-id="'+row.id+'"]').count(),0);
 console.log('Workbench loaded from the folder in a new tab, without a row of its own');
 // Dismiss workspace trust if VS Code asks before opening project files.
 const trust=tab.getByRole('button',{name:/Yes, I trust/});
 if (await trust.count()) await trust.click();
 await tab.getByRole('treeitem',{name:/smoke.txt/}).first().dblclick({timeout:30000});
 await tab.locator('.monaco-editor .view-line').filter({hasText:'hello from Socrates'}).click();
 await tab.keyboard.press('Control+End');
 await tab.keyboard.type('saved inside the embedded IDE', {delay:30});
 await tab.keyboard.press('Escape');
 await tab.keyboard.press('Control+s');
 const savedBy = Date.now() + 10000;
 while (readFileSync(join(project,'smoke.txt'),'utf8').length < 25 && Date.now() < savedBy) await s.page.waitForTimeout(250);
 assert.equal(readFileSync(join(project,'smoke.txt'),'utf8'),'hello from Socrates\nsaved inside the embedded IDE');
 console.log('File edited and saved');
 await tab.keyboard.press('F1');
 await tab.keyboard.type('Terminal: Create New Terminal', {delay:30});
 await tab.keyboard.press('Enter');
 await tab.getByRole('button', {name:'Trust Folder & Continue',exact:true}).click({timeout:15000}).catch(()=>{});
 const terminal = tab.locator('.xterm-screen').first();
 await terminal.waitFor({timeout:15000});
 await terminal.click();
 await tab.keyboard.type("printf 'terminal works' > terminal-check.txt", {delay:30});
 await tab.keyboard.press('Enter');
 await s.page.waitForTimeout(1000);
 assert.equal(readFileSync(join(project,'terminal-check.txt'),'utf8'),'terminal works');
 console.log('Integrated terminal executed a command');
 await tab.screenshot({path:'e2e/out/vscode.png'});
 await s.restart();
 await tab.reload();
 await tab.locator('.monaco-workbench').waitFor({timeout:90000});
 await tab.close();
 await s.page.reload();
 console.log('IDE survived Socrates restart');
 await s.page.locator('.so-row[data-id="'+shell.session.id+'"]').click();
 await s.page.locator('.xterm-screen').waitFor();
 console.log('Switch to shell passed');
 tab = await openTab(() => ideButton.click());
 // The shell stays on the stage: the editor is beside it, in its tab.
 assert(await s.page.locator('.xterm-screen').isVisible());
 await tab.close();
 const again = (await (await s.context.request.get(s.url+'/api/sessions')).json()).sessions.filter((one)=>one.harness==='vscode');
 assert.equal(again.length,1);
 console.log('Switch back to the same IDE passed');
 s.server.kill('SIGTERM');
 await s.exited;
 killTmux(s.data);
 await s.restart();
 await s.page.reload();
 tab = await openTab(() => ideButton.click());
 await tab.locator('.tab', {hasText:'smoke.txt'}).waitFor({timeout:30000});
 await tab.close();
 console.log('IDE resumed after tmux loss, with its open editor restored');
 // The empty editor opens in a tab from the drawer, and so does its row.
 tab = await openTab(() => s.page.locator('#blankIDE').click());
 assert.match(tab.url(), /\/ide\/[^/]+\/\?ew=true/);
 await tab.close();
 const blank = (await (await s.context.request.get(s.url+'/api/sessions')).json()).sessions.find((one)=>one.harness==='vscode'&&one.workdir_mode==='dynamic');
 await s.page.reload();
 const blankRow = s.page.locator('.so-row[data-id="'+blank.id+'"] .so-row__open');
 const more = s.page.locator('.so-folder[data-folder="#more"]');
 await more.waitFor();
 if (await more.getAttribute('data-open') === null) await more.locator('.so-folder__head').click();
 await blankRow.waitFor();
 tab = await openTab(() => blankRow.click());
 await tab.close();
 assert.notEqual(new URL(s.page.url()).hash, '#'+blank.id);
 console.log('The empty editor opens in a new tab from the drawer and from its row');
 assert((await s.context.request.delete(s.url+'/api/sessions/'+blank.id)).ok());
 // Closing the editor keeps what it remembers for the folder.
 assert((await s.context.request.delete(s.url+'/api/sessions/'+row.id)).ok());
 await s.page.reload();
 tab = await openTab(() => ideButton.click(), 60000);
 await tab.locator('.tab', {hasText:'smoke.txt'}).waitFor({timeout:60000});
 await tab.close();
 console.log('A new editor on the same folder restored its state');
 const last = (await (await s.context.request.get(s.url+'/api/sessions')).json()).sessions;
 for (const one of last) assert((await s.context.request.delete(s.url+'/api/sessions/'+one.id)).ok());
} catch (err) {
 console.log('Server:',s.log.join(''));
 console.log('Browser errors:',s.errors);
 await s.page.screenshot({path:'e2e/out/vscode-failure.png'}).catch(()=>{});
 throw err;
} finally {
 await s.stop();
 assert.equal(totals.fail, 0, 'smoke test cleanup must leave no sessions');
}

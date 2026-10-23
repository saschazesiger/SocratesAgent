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
 const ideButton = s.page.locator('.folder[data-folder="'+project+'"] > .folder-bar > .folder-ide');
 await ideButton.click();
 const iframe=s.page.frameLocator('iframe.ide-frame');
 await iframe.locator('.monaco-workbench').waitFor({timeout:45000});
 const list = (await (await s.context.request.get(s.url+'/api/sessions')).json()).sessions;
 const row = list.find((one)=>one.harness==='vscode');
 assert(row && row.workdir===project);
 assert.equal(await s.page.locator('.so-row[data-id="'+row.id+'"]').count(),0);
 console.log('Workbench loaded from the folder, without a row of its own');
 // Dismiss workspace trust if VS Code asks before opening project files.
 const trust=iframe.getByRole('button',{name:/Yes, I trust/});
 if (await trust.count()) await trust.click();
 await iframe.getByRole('treeitem',{name:/smoke.txt/}).first().dblclick({timeout:30000});
 await iframe.locator('.monaco-editor .view-line').filter({hasText:'hello from Socrates'}).click();
 await s.page.keyboard.press('Control+End');
 await s.page.keyboard.type('saved inside the embedded IDE', {delay:30});
 await s.page.keyboard.press('Escape');
 await s.page.keyboard.press('Control+s');
 const savedBy = Date.now() + 10000;
 while (readFileSync(join(project,'smoke.txt'),'utf8').length < 25 && Date.now() < savedBy) await s.page.waitForTimeout(250);
 assert.equal(readFileSync(join(project,'smoke.txt'),'utf8'),'hello from Socrates\nsaved inside the embedded IDE');
 console.log('File edited and saved');
 await s.page.keyboard.press('F1');
 await s.page.keyboard.type('Terminal: Create New Terminal', {delay:30});
 await s.page.keyboard.press('Enter');
 await iframe.getByRole('button', {name:'Trust Folder & Continue',exact:true}).click({timeout:15000}).catch(()=>{});
 const terminal = iframe.locator('.xterm-screen').first();
 await terminal.waitFor({timeout:15000});
 await terminal.click();
 await s.page.keyboard.type("printf 'terminal works' > terminal-check.txt", {delay:30});
 await s.page.keyboard.press('Enter');
 await s.page.waitForTimeout(1000);
 assert.equal(readFileSync(join(project,'terminal-check.txt'),'utf8'),'terminal works');
 console.log('Integrated terminal executed a command');
 await s.page.screenshot({path:'e2e/out/vscode.png'});
 await s.restart();
 await s.page.reload();
 await s.page.frameLocator('iframe.ide-frame').locator('.monaco-workbench').waitFor({timeout:90000});
 console.log('IDE survived Socrates restart');
 await s.page.locator('.so-row[data-id="'+shell.session.id+'"]').click();
 await s.page.locator('.xterm-screen').waitFor();
 assert.equal(await s.page.locator('iframe.ide-frame').count(),0);
 console.log('Switch to shell passed');
 await ideButton.click();
 await s.page.frameLocator('iframe.ide-frame').locator('.monaco-workbench').waitFor({timeout:45000});
 const again = (await (await s.context.request.get(s.url+'/api/sessions')).json()).sessions.filter((one)=>one.harness==='vscode');
 assert.equal(again.length,1);
 console.log('Switch back to the same IDE passed');
 s.server.kill('SIGTERM');
 await s.exited;
 killTmux(s.data);
 await s.restart();
 await s.page.reload();
 await s.page.frameLocator('iframe.ide-frame').locator('.monaco-workbench').waitFor({timeout:45000});
 await s.page.frameLocator('iframe.ide-frame').locator('.tab', {hasText:'smoke.txt'}).waitFor({timeout:30000});
 console.log('IDE resumed after tmux loss, with its open editor restored');
 // Closing the editor keeps what it remembers for the folder.
 assert((await s.context.request.delete(s.url+'/api/sessions/'+row.id)).ok());
 await s.page.reload();
 await ideButton.click();
 await s.page.frameLocator('iframe.ide-frame').locator('.tab', {hasText:'smoke.txt'}).waitFor({timeout:60000});
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

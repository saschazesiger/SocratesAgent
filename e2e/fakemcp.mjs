#!/usr/bin/env node
// fakemcp.mjs: the smallest MCP server on stdio, for the browser suite's test
// of a connection (Admin's Test, a chat's info button). It answers
// initialize, tools/list, prompts/list and resources/list with fixed data,
// one JSON-RPC message per line, and says on stderr that it started.
//
//   node e2e/fakemcp.mjs            a server with two tools and one prompt
//   node e2e/fakemcp.mjs --fail     a server that dies before it answers
import { createInterface } from 'node:readline';

if (process.argv.includes('--fail')) {
  process.stderr.write('fakemcp: no token in FAKEMCP_TOKEN\n');
  process.exit(3);
}
process.stderr.write('fakemcp: listening on stdio\n');

const TOOLS = [
  { name: 'echo', title: 'Echo', description: 'Says back what it is given.', inputSchema: { type: 'object', properties: { text: { type: 'string' } } } },
  { name: 'add', description: 'Adds two numbers.', inputSchema: { type: 'object', properties: { a: { type: 'number' }, b: { type: 'number' } } } },
];

const send = (msg) => process.stdout.write(JSON.stringify({ jsonrpc: '2.0', ...msg }) + '\n');

createInterface({ input: process.stdin }).on('line', (line) => {
  let msg;
  try { msg = JSON.parse(line); } catch { return; }
  if (msg.id === undefined) return;
  switch (msg.method) {
    case 'initialize':
      send({ id: msg.id, result: {
        protocolVersion: (msg.params && msg.params.protocolVersion) || '2025-06-18',
        capabilities: { tools: {}, prompts: {} },
        serverInfo: { name: 'fakemcp', title: 'Fake MCP', version: '1.2.3' },
        instructions: 'Use echo to repeat a text.\n\nUse add for sums.',
      } });
      break;
    case 'tools/list': send({ id: msg.id, result: { tools: TOOLS } }); break;
    case 'prompts/list': send({ id: msg.id, result: { prompts: [{ name: 'greet', description: 'A greeting.' }] } }); break;
    case 'resources/list': send({ id: msg.id, result: { resources: [] } }); break;
    case 'ping': send({ id: msg.id, result: {} }); break;
    default: send({ id: msg.id, error: { code: -32601, message: 'no method ' + msg.method } });
  }
});

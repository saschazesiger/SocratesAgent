// The chat dialog of the Tests page (docs/design/TEST-MODE.md, "The Tests
// page", the chat dialog): Fix in a chat, Write a test with an agent, Change
// it with an agent. mount(ctx) hands back { open(spec, opener) } with spec
// { kind: 'fix-bug' | 'fix-cell' | 'write-test' | 'change-test', subject }.
//
// Stub: replaced by the full view.

import { toast } from './api.js';

export function mount() {
  return {
    open() {
      toast('Coming soon: a chat that starts with this already written.', 'info');
    },
  };
}

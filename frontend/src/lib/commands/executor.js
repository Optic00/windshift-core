import { navigate } from '../router.js';

/**
 * Run a command. Providers should set either `url` (for navigation) or
 * `execute` (for everything else). Legacy commands registered via
 * contextCommands.js carry `type: 'context-action'` + `action: fn` — handled
 * here as a single branch so providers don't need to know about it.
 *
 * @param {any} cmd
 * @returns {Promise<void>}
 */
export async function executeCommand(cmd) {
  if (!cmd) return;
  if (typeof cmd.execute === 'function') {
    await cmd.execute();
    return;
  }
  // Legacy contextCommands shape: { type: 'context-action', action: fn }
  if (cmd.type === 'context-action' && typeof cmd.action === 'function') {
    await cmd.action();
    return;
  }
  if (cmd.url) {
    navigate(cmd.url);
    return;
  }
  if (typeof cmd.action === 'function') {
    await cmd.action();
    return;
  }
  console.warn('[command-palette] no executor for command:', cmd);
}

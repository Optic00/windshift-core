import { BUCKET } from '../buckets.js';
import { deriveLegacyBucket } from '../types.js';

/**
 * Wrap commands pushed into the palette from elsewhere in the app via
 * `registerContextCommands` (utils/contextCommands.js). These are typically
 * focused-entity actions (e.g. item detail), which is why the default bucket
 * here is ITEM_ACTIONS — though individual commands can override with their
 * own `bucket` field.
 *
 * The provider closure captures the live $contextCommands array from the
 * palette and yields its current contents on every invocation. The palette
 * re-runs providers when `query` changes, so registrations made after the
 * palette opens are picked up automatically.
 */
export function makeExternalProvider(getContextCommands) {
  return function externalProvider(_ctx) {
    const commands = getContextCommands() || [];
    return commands.map((cmd) => ({
      ...cmd,
      bucket:
        cmd.bucket ||
        (cmd.type === 'context-action' ? BUCKET.ITEM_ACTIONS : deriveLegacyBucket(cmd)),
      _isContextCommand: true,
    }));
  };
}

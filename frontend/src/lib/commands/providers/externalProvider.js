import { BUCKET } from '../buckets.js';
import { deriveLegacyBucket } from '../types.js';

/** Adapt live context-command registrations for the palette. Commands default
 * to ITEM_ACTIONS but can choose a bucket; reruns pick up new registrations. */
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

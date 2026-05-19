/**
 * Call each provider with ctx, flatten the resulting commands, and stamp
 * provider-relative insertion order. Ranking, filtering and capping live in
 * the palette's $derived chain — this function is purely command production.
 *
 * @param {import('./types.js').CommandContext} ctx
 * @param {Array<(ctx:any) => any[]>} providers
 * @returns {any[]}
 */
export function buildCommands(ctx, providers) {
  const out = [];
  let seq = 0;
  for (const provider of providers) {
    let cmds;
    try {
      cmds = provider(ctx) || [];
    } catch (err) {
      console.error('[command-palette] provider failed:', provider?.name, err);
      cmds = [];
    }
    for (const cmd of cmds) {
      if (cmd && (!cmd.isAvailable || cmd.isAvailable(ctx))) {
        cmd._seq = seq++;
        out.push(cmd);
      }
    }
  }
  return out;
}

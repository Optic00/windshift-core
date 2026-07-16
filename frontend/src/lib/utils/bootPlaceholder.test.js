import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { describe, expect, test } from 'vitest';

describe('pre-mount boot placeholder', () => {
  test('stays in document flow and is removed before Svelte mounts', async () => {
    const [html, main] = await Promise.all([
      readFile(path.resolve('index.html'), 'utf8'),
      readFile(path.resolve('src/main.js'), 'utf8'),
    ]);
    const bootStyles = html.match(/#windshift-boot\s*\{([^}]*)\}/)?.[1] ?? '';

    expect(html).toContain('id="windshift-boot"');
    expect(bootStyles).not.toMatch(/position\s*:\s*(?:fixed|absolute)/);
    expect(main.indexOf('target.replaceChildren()')).toBeGreaterThan(-1);
    expect(main.indexOf('target.replaceChildren()')).toBeLessThan(main.indexOf('mount(App'));
  });
});

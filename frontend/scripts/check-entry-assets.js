#!/usr/bin/env node
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import process from 'node:process';
import { fileURLToPath } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const defaultIndexPath = path.resolve(scriptDir, '../dist/index.html');

const forbiddenEntryAssets = [
  { name: 'Excalidraw', pattern: /(?:excalidraw|mermaid-to-excalidraw)/i },
  { name: 'React', pattern: /(?:^|[/_.-])react(?:-dom)?(?:[/_.-]|$)/i },
  { name: 'SvelteFlow', pattern: /(?:svelteflow|xyflow)/i },
  { name: 'D3', pattern: /(?:^|[/_.-])d3(?:[/_.-]|$)/i },
];

/**
 * Return optional feature assets that index.html loads before route-level
 * dynamic imports have a chance to run.
 *
 * @param {string} html
 * @returns {{ name: string, asset: string }[]}
 */
export function findForbiddenEntryAssets(html) {
  const assets = new Set();
  const assetPattern = /<(?:script|link)\b[^>]*?\b(?:src|href)=["']([^"']+)["'][^>]*>/gi;
  let match;

  while ((match = assetPattern.exec(html)) !== null) {
    assets.add(match[1]);
  }

  const violations = [];
  for (const asset of assets) {
    for (const forbidden of forbiddenEntryAssets) {
      if (forbidden.pattern.test(asset)) {
        violations.push({ name: forbidden.name, asset });
      }
    }
  }
  return violations;
}

async function main() {
  const indexPath = process.argv[2] ? path.resolve(process.argv[2]) : defaultIndexPath;
  const html = await readFile(indexPath, 'utf8');
  const violations = findForbiddenEntryAssets(html);

  if (violations.length > 0) {
    console.error('Optional feature assets leaked into the initial application entry:');
    for (const violation of violations) {
      console.error(`- ${violation.name}: ${violation.asset}`);
    }
    process.exitCode = 1;
    return;
  }

  console.log('Entry asset check passed: optional graph/editor dependencies remain lazy.');
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    console.error(error);
    process.exitCode = 1;
  });
}

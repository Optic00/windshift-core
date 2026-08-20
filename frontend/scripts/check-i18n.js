#!/usr/bin/env node

/**
 * i18n Validation Script
 *
 * Validates locale files against English (reference locale) and detects:
 * - Missing or extra keys in non-English locales
 * - Source keys referenced in code but missing from English catalog
 * - Placeholder mismatches between English and other locales
 * - Untranslated English carryovers in non-English locales
 *
 * Usage: node frontend/scripts/check-i18n.js [--verbose]
 */

import { glob, readdir, readFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { mergeInto } from '../src/lib/locales/createLocale.js';

const __dirname = dirname(fileURLToPath(import.meta.url));
const LOCALES_DIR = join(__dirname, '..', 'src', 'lib', 'locales');
const SRC_DIR = join(__dirname, '..', 'src');
const REFERENCE_LOCALE = 'en';

// These values intentionally retain product names, code syntax, URLs, or sample identifiers.
const INTENTIONAL_CARRYOVERS = new Set([
  'about.libCharmbracelet',
  'channel.microsoftOrGoogle',
  'collections.queryPlaceholder',
  'iterations.iterationNamePlaceholder',
  'jiraImport.form.urlCloud',
  'jiraImport.form.urlDatacenter',
  'jiraImport.title.cloud',
  'jiraImport.title.datacenter',
  'portal.qlQueryFormPlaceholder',
  'portal.qlQueryPlaceholder',
  'settings.sso.title',
  'workspaces.customers.placeholders.phone',
]);
const ENGLISH_FUNCTION_WORDS = new Set([
  'a',
  'an',
  'and',
  'are',
  'for',
  'from',
  'is',
  'of',
  'that',
  'the',
  'this',
  'to',
  'with',
  'you',
  'your',
]);

function flattenKeys(obj, prefix = '') {
  const keys = [];
  for (const [key, value] of Object.entries(obj)) {
    const fullKey = prefix ? `${prefix}.${key}` : key;
    if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
      keys.push(...flattenKeys(value, fullKey));
    } else {
      keys.push(fullKey);
    }
  }
  return keys;
}

function flattenAll(obj, prefix = '') {
  const entries = [];
  for (const [key, value] of Object.entries(obj)) {
    const fullKey = prefix ? `${prefix}.${key}` : key;
    if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
      entries.push(...flattenAll(value, fullKey));
    } else {
      entries.push([fullKey, value]);
    }
  }
  return entries;
}

function extractPlaceholders(str) {
  if (typeof str !== 'string') return [];
  const matches = str.match(/\{(\w+)\}/g);
  return matches ? matches.map((m) => m.slice(1, -1)).sort() : [];
}

function findSourceFile(key, fileMap) {
  for (const [filename, keys] of Object.entries(fileMap)) {
    if (keys.has(key)) return filename;
  }
  return '?';
}

async function loadLocaleFiles(localeCode) {
  const localeDir = join(LOCALES_DIR, localeCode);
  const files = (await readdir(localeDir))
    .filter((f) => f.endsWith('.js') && f !== 'index.js')
    .sort((a, b) => {
      const rank = (file) =>
        file === 'review.js' ? 3 : file === 'quality.js' ? 2 : file === 'supplemental.js' ? 1 : 0;
      return rank(a) - rank(b) || a.localeCompare(b);
    });

  const merged = {};
  const fileKeyMap = {};

  for (const file of files) {
    const mod = await import(join(localeDir, file));
    const data = mod.default || mod;
    const keys = new Set(flattenKeys(data));
    fileKeyMap[file] = keys;
    mergeInto(merged, data);
  }

  return { merged, fileKeyMap };
}

async function extractSourceKeys() {
  const pattern = join(SRC_DIR, '**/*.{svelte,js,ts}');
  const files = await Array.fromAsync(
    glob(pattern, { exclude: (f) => f.includes('node_modules') || f.includes('locales') })
  );
  const keys = new Set();

  for (const file of files) {
    const content = await readFile(file, 'utf-8');
    const matches = content.matchAll(/\bt\(['"]([a-zA-Z][a-zA-Z0-9_.]+)['"]/g);
    for (const match of matches) {
      keys.add(match[1]);
    }
  }

  return keys;
}

function detectCarryovers(english, other, _localeCode) {
  const carryovers = [];
  const enEntries = Object.fromEntries(
    flattenAll(english).filter(([, v]) => typeof v === 'string')
  );

  for (const [key, value] of flattenAll(other)) {
    if (INTENTIONAL_CARRYOVERS.has(key)) continue;
    if (typeof value !== 'string') continue;
    const enValue = enEntries[key];
    if (!enValue) continue;

    const words = value.split(/\s+/);
    const wordCount = words.length;

    if (enValue === value && wordCount >= 3) {
      carryovers.push({ key, value, enValue, matchRatio: 1 });
      continue;
    }

    if (enValue === value) continue;

    const englishWordsInValue = value.match(/[A-Za-z][A-Za-z'-]*/g) ?? [];
    if (englishWordsInValue.length >= 3) {
      const enWords = enValue.match(/[A-Za-z][A-Za-z'-]*/g) ?? [];
      const matching = englishWordsInValue.filter((w) => enWords.includes(w));
      const matchingWords = matching.length;
      const matchRatio = matchingWords / englishWordsInValue.length;
      const functionWordMatches = matching.filter((word) =>
        ENGLISH_FUNCTION_WORDS.has(word.toLowerCase())
      ).length;
      if (matchingWords >= 4 && functionWordMatches >= 2 && matchRatio > 0.7) {
        carryovers.push({ key, value, enValue, matchRatio });
      }
    }
  }

  return carryovers;
}

async function main() {
  const verbose = process.argv.includes('--verbose');

  const localeDirs = (await readdir(LOCALES_DIR, { withFileTypes: true }))
    .filter((d) => d.isDirectory())
    .map((d) => d.name);

  if (!localeDirs.includes(REFERENCE_LOCALE)) {
    console.error(`Reference locale "${REFERENCE_LOCALE}" not found.`);
    process.exit(1);
  }

  let exitCode = 0;

  const ref = await loadLocaleFiles(REFERENCE_LOCALE);
  const refLeafKeys = new Set(flattenKeys(ref.merged));
  const refEntries = Object.fromEntries(
    flattenAll(ref.merged).filter(([, v]) => typeof v === 'string')
  );

  console.log(`\n  Reference: ${REFERENCE_LOCALE} (${refLeafKeys.size} leaf keys)\n`);

  // --- Check 1: Source keys missing from English ---
  console.log('  --- Source key presence ---');
  const sourceKeys = await extractSourceKeys();
  const missingFromEn = [...sourceKeys]
    .filter((k) => !refLeafKeys.has(k))
    .filter((k) => k.includes('.'))
    .sort();

  if (missingFromEn.length > 0) {
    console.log(`  ✗ ${missingFromEn.length} source key(s) missing from English catalog:`);
    for (const key of missingFromEn) {
      console.log(`      - ${key}`);
    }
    exitCode = 1;
  } else {
    console.log('  ✓ All source keys present in English catalog');
  }
  console.log('');

  // --- Check 2: Non-English locale parity ---
  console.log('  --- Locale key parity ---');
  const otherLocales = localeDirs.filter((l) => l !== REFERENCE_LOCALE).sort();
  let totalMissing = 0;
  let totalExtra = 0;

  for (const locale of otherLocales) {
    const loc = await loadLocaleFiles(locale);
    const locKeys = new Set(flattenKeys(loc.merged));

    const missing = [...refLeafKeys].filter((k) => !locKeys.has(k)).sort();
    const extra = [...locKeys].filter((k) => !refLeafKeys.has(k)).sort();

    totalMissing += missing.length;
    totalExtra += extra.length;

    const coverage = (((refLeafKeys.size - missing.length) / refLeafKeys.size) * 100).toFixed(1);

    if (missing.length === 0 && extra.length === 0) {
      console.log(`  ✓ ${locale}  ${coverage}% coverage  (${locKeys.size} keys)`);
    } else {
      console.log(
        `  ✗ ${locale}  ${coverage}% coverage  (${locKeys.size} keys, ${missing.length} missing, ${extra.length} extra)`
      );
    }

    if (missing.length > 0 && verbose) {
      const byFile = {};
      for (const key of missing) {
        const file = findSourceFile(key, ref.fileKeyMap);
        (byFile[file] ??= []).push(key);
      }
      for (const [file, keys] of Object.entries(byFile).sort()) {
        console.log(`      missing in ${file}:`);
        for (const key of keys) {
          console.log(`        - ${key}`);
        }
      }
    }
  }
  console.log('');

  // --- Check 3: Placeholder mismatches ---
  console.log('  --- Placeholder parity ---');
  let placeholderErrors = 0;

  for (const locale of otherLocales) {
    const loc = await loadLocaleFiles(locale);
    const locEntries = Object.fromEntries(
      flattenAll(loc.merged).filter(([, v]) => typeof v === 'string')
    );

    const mismatches = [];
    for (const [key, enValue] of Object.entries(refEntries)) {
      const locValue = locEntries[key];
      if (!locValue) continue;

      const enPlaceholders = extractPlaceholders(enValue);
      const locPlaceholders = extractPlaceholders(locValue);

      if (enPlaceholders.length === 0 && locPlaceholders.length === 0) continue;

      const locSet = new Set(locPlaceholders);

      const missing = enPlaceholders.filter((p) => !locSet.has(p) && p !== 'plural');
      const enSet = new Set(enPlaceholders.filter((p) => p !== 'plural'));
      const extra = locPlaceholders.filter((p) => p !== 'plural' && !enSet.has(p));

      if (missing.length > 0 || extra.length > 0) {
        mismatches.push({ key, enValue, locValue, missing, extra });
      }
    }

    if (mismatches.length > 0) {
      console.log(`  ✗ ${locale}: ${mismatches.length} placeholder mismatch(es):`);
      for (const m of mismatches) {
        const details = [];
        if (m.missing.length > 0) details.push(`missing: {${m.missing.join('}, {')}}`);
        if (m.extra.length > 0) details.push(`extra: {${m.extra.join('}, {')}}`);
        console.log(`      ${m.key} — ${details.join(', ')}`);
        if (verbose) {
          console.log(`        EN: ${m.enValue}`);
          console.log(`        ${locale}: ${m.locValue}`);
        }
      }
      placeholderErrors += mismatches.length;
      exitCode = 1;
    } else {
      console.log(`  ✓ ${locale}: all placeholders match`);
    }
  }

  if (placeholderErrors === 0) {
    console.log('  ✓ All placeholders match across locales');
  }
  console.log('');

  // --- Check 4: Untranslated carryovers ---
  console.log('  --- Untranslated carryover detection ---');
  let carryoverTotal = 0;

  for (const locale of otherLocales) {
    const loc = await loadLocaleFiles(locale);
    const carryovers = detectCarryovers(ref.merged, loc.merged, locale);

    if (carryovers.length > 0) {
      console.log(`  ✗ ${locale}: ${carryovers.length} suspected carryover(s):`);
      if (verbose) {
        for (const c of carryovers.slice(0, 20)) {
          console.log(`      ${c.key}`);
        }
        if (carryovers.length > 20) {
          console.log(`      ... and ${carryovers.length - 20} more`);
        }
      }
      carryoverTotal += carryovers.length;
      exitCode = 1;
    } else {
      console.log(`  ✓ ${locale}: no obvious carryovers detected`);
    }
  }

  if (carryoverTotal === 0) {
    console.log('  ✓ No carryovers detected');
  }
  console.log('');

  // --- Summary ---
  if (totalMissing > 0 || totalExtra > 0) {
    exitCode = 1;
  }

  if (exitCode === 0) {
    console.log('  All checks passed!\n');
  } else {
    const issues = [];
    if (missingFromEn.length > 0)
      issues.push(`${missingFromEn.length} source key(s) missing from English`);
    if (totalMissing > 0) issues.push(`${totalMissing} missing locale key(s)`);
    if (totalExtra > 0) issues.push(`${totalExtra} extra locale key(s)`);
    if (placeholderErrors > 0) issues.push(`${placeholderErrors} placeholder mismatch(es)`);
    if (carryoverTotal > 0) issues.push(`${carryoverTotal} suspected carryover(s)`);
    console.log(`  ${issues.join(', ')}`);
    console.log('');
    process.exit(1);
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});

import { bucketRank } from './buckets.js';

const TIER_EXACT = 140;
const TIER_TOKEN = 130;
const TIER_PREFIX = 120;
const TIER_SUBSTRING = 100;
const FUZZY_PER_CHAR = 10;

const TOKEN_SPLIT = /[^a-z0-9]+/;

/**
 * Score a single text against a query. Higher is better; 0 means no match.
 * Tiers: exact > token-equality > prefix > substring > fuzzy.
 *
 * The token tier is what stops `dashboard` from scoring like `board`: if the
 * query equals one of the text's whitespace/punct-separated tokens it wins
 * over plain substring containment.
 *
 * @param {string} query
 * @param {string} text
 * @returns {number}
 */
export function scoreText(query, text) {
  if (!query || !text) return 0;
  const q = query.toLowerCase();
  const t = text.toLowerCase();

  if (t === q) return TIER_EXACT;

  const tokens = t.split(TOKEN_SPLIT);
  if (tokens.includes(q)) return TIER_TOKEN;
  if (tokens.some((tok) => tok.startsWith(q))) return TIER_PREFIX;

  if (t.startsWith(q)) return TIER_PREFIX - 5;
  if (t.includes(q)) return TIER_SUBSTRING;

  // Fuzzy fallback: characters appear in order anywhere in text.
  let qi = 0;
  let score = 0;
  for (let i = 0; i < t.length && qi < q.length; i++) {
    if (t[i] === q[qi]) {
      score += FUZZY_PER_CHAR;
      qi++;
    }
  }
  return qi === q.length ? Math.min(score, TIER_SUBSTRING - 1) : 0;
}

/**
 * Best of label / keyword / description scores. Keyword scores decay by
 * index so authors can express importance via order.
 *
 * @param {string} query
 * @param {{label:string, description?:string, keywords?:string[]}} cmd
 * @returns {number}
 */
export function scoreCommand(query, cmd) {
  if (!query) return 0;
  const label = scoreText(query, cmd.label);
  const desc = cmd.description ? scoreText(query, cmd.description) : 0;
  let kw = 0;
  if (cmd.keywords?.length) {
    for (let i = 0; i < cmd.keywords.length; i++) {
      const decay = Math.max(0, 20 - i * 4);
      const s = scoreText(query, cmd.keywords[i]);
      if (s === 0) continue;
      const adjusted = s + decay;
      if (adjusted > kw) kw = adjusted;
    }
  }
  return Math.max(label, kw, desc);
}

/**
 * Comparator for sorting commands. Primary: bucket order (query-dependent).
 * Secondary: descending text score. Tertiary: provider insertion order
 * (lower `_seq` wins).
 *
 * @param {string} query
 * @returns {(a, b) => number}
 */
export function compareCommands(query) {
  return (a, b) => {
    const ar = bucketRank(a.bucket, query);
    const br = bucketRank(b.bucket, query);
    if (ar !== br) return ar - br;
    if ((b._score || 0) !== (a._score || 0)) return (b._score || 0) - (a._score || 0);
    return (a._seq || 0) - (b._seq || 0);
  };
}

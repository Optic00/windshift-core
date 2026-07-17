export const STEPS_SHORTCUT_ALPHABET = '1234567890ABCDEFGHIJKLMNOPQRSTUVWXYZ';

/**
 * Create fixed-width shortcut codes for a list of test cases.
 *
 * Most lists use a single key (1-9, 0, then A-Z). If the list is larger than
 * the available one-key alphabet, every code grows to the same width so no
 * code can be mistaken for the prefix of another one.
 */
export function createStepsShortcutCodes(count) {
  if (!Number.isInteger(count) || count <= 0) return [];

  const base = STEPS_SHORTCUT_ALPHABET.length;
  const width = Math.max(1, Math.ceil(Math.log(count) / Math.log(base)));

  return Array.from({ length: count }, (_, index) => {
    let remaining = index;
    let code = '';

    for (let position = 0; position < width; position += 1) {
      code = STEPS_SHORTCUT_ALPHABET[remaining % base] + code;
      remaining = Math.floor(remaining / base);
    }

    return code;
  });
}

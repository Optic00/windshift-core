export const STEPS_SHORTCUT_ALPHABET = '1234567890ABCDEFGHIJKLMNOPQRSTUVWXYZ';

/** Create fixed-width test-case shortcuts so no code prefixes another. */
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

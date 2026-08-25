const rasterDataURI = /^data:image\/(?:png|jpeg|gif|webp);base64,[a-z0-9+/]+=*$/i;

const commonURLSchemes = ['http', 'https'] as const;
const linkOnlyURLSchemes = ['mailto', 'tel'] as const;

export const markdownURLSchemes = [
  ...commonURLSchemes,
  ...linkOnlyURLSchemes,
  'page',
  'data',
] as const;

const commonSchemes = new Set<string>(commonURLSchemes);
const linkOnlySchemes = new Set<string>(linkOnlyURLSchemes);

function hasUnsafeCharacters(value: string): boolean {
  if (value.includes('\\') || /%5c/i.test(value)) return true;
  for (const character of value) {
    const code = character.charCodeAt(0);
    if (code <= 0x20 || code === 0x7f) return true;
  }
  return false;
}

type MarkdownURLOptions = {
  image?: boolean;
  allowBlobImage?: boolean;
};

/** Validate Markdown destinations used by readonly rendering and the editor. */
export function isSafeMarkdownURL(
  value: string,
  { image = false, allowBlobImage = false }: MarkdownURLOptions = {}
): boolean {
  if (!value || value.startsWith('//') || hasUnsafeCharacters(value)) return false;
  if (value.startsWith('#') || value.startsWith('/')) return true;
  if (image && rasterDataURI.test(value)) return true;
  if (image && allowBlobImage && /^blob:https?:/i.test(value)) return true;

  const scheme = /^([a-z][a-z0-9+.-]*):/i.exec(value)?.[1]?.toLowerCase();
  if (!scheme) return /^[^/:?#\\][^:\\]*$/.test(value);
  if (commonSchemes.has(scheme)) return true;
  if (!image && linkOnlySchemes.has(scheme)) return true;
  return !image && scheme === 'page' && /^page:[0-9]+$/i.test(value);
}

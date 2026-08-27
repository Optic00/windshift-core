import { readdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const defaultFontsDir = path.resolve(
  scriptDir,
  '../node_modules/@excalidraw/excalidraw/dist/prod/fonts'
);

export const EXCALIDRAW_ASSET_ROUTE = '/excalidraw-assets/';
const fontRoute = `${EXCALIDRAW_ASSET_ROUTE}fonts/`;
// Excalidraw fetches this large CJK fallback from its version-pinned CDN.
const cdnOnlyFontFamilies = new Set(['Xiaolai']);

function isCdnOnlyFont(relativePath) {
  const [fontFamily] = relativePath.split(/[\\/]/);
  return cdnOnlyFontFamilies.has(fontFamily);
}

export function collectExcalidrawFontAssets(fontsDir = defaultFontsDir) {
  const assets = [];

  function visit(directory, relativeDirectory = '') {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const relativePath = path.join(relativeDirectory, entry.name);
      const absolutePath = path.join(directory, entry.name);
      if (entry.isDirectory()) {
        if (isCdnOnlyFont(relativePath)) continue;
        visit(absolutePath, relativePath);
      } else if (entry.isFile() && entry.name.endsWith('.woff2')) {
        assets.push({
          absolutePath,
          fileName: path.posix.join('excalidraw-assets/fonts', relativePath),
        });
      }
    }
  }

  visit(fontsDir);
  return assets;
}

export function excalidrawAssetsPlugin({ fontsDir = defaultFontsDir } = {}) {
  let shouldEmitAssets = false;

  return {
    name: 'windshift-excalidraw-assets',

    configResolved(config) {
      shouldEmitAssets = config.command === 'build';
    },

    configureServer(server) {
      const resolvedFontsDir = path.resolve(fontsDir);
      server.middlewares.use((request, response, next) => {
        let pathname;
        try {
          pathname = new URL(request.url || '/', 'http://vite.local').pathname;
        } catch {
          next();
          return;
        }
        if (!pathname.startsWith(fontRoute)) {
          next();
          return;
        }

        let relativePath;
        try {
          relativePath = decodeURIComponent(pathname.slice(fontRoute.length));
        } catch {
          next();
          return;
        }
        if (isCdnOnlyFont(relativePath)) {
          next();
          return;
        }
        const fontPath = path.resolve(resolvedFontsDir, relativePath);
        if (
          !fontPath.startsWith(`${resolvedFontsDir}${path.sep}`) ||
          path.extname(fontPath) !== '.woff2'
        ) {
          next();
          return;
        }

        try {
          const font = readFileSync(fontPath);
          response.statusCode = 200;
          response.setHeader('Content-Type', 'font/woff2');
          response.setHeader('Cache-Control', 'public, max-age=31536000, immutable');
          response.setHeader('Content-Length', String(font.byteLength));
          response.end(font);
        } catch {
          next();
        }
      });
    },

    buildStart() {
      if (!shouldEmitAssets) return;
      for (const asset of collectExcalidrawFontAssets(fontsDir)) {
        this.emitFile({
          type: 'asset',
          fileName: asset.fileName,
          source: readFileSync(asset.absolutePath),
        });
      }
    },
  };
}

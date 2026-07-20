import { publicBaseURL } from '../runtime/contextPath.js';

export const LEGACY_DARK_CANVAS_COLOR = '#1e1e1e';
export const DEFAULT_EXCALIDRAW_CANVAS_COLOR = '#ffffff';

export function excalidrawAssetPath() {
  return `${publicBaseURL()}/excalidraw-assets/`;
}

/** @param {any} [target] */
export function configureExcalidrawAssets(target = window) {
  const assetPath = excalidrawAssetPath();
  target.EXCALIDRAW_ASSET_PATH = assetPath;
  return assetPath;
}

export function prepareExcalidrawInitialData(initialData) {
  const scene = initialData || {};
  const appState = { ...(scene.appState || {}) };

  // Older Windshift builds forced Excalidraw's backing canvas to the same
  // dark color as its default stroke. Excalidraw applies dark mode by
  // inverting the complete canvas, so those identical colors stayed
  // identical and newly drawn shapes were invisible.
  if (
    typeof appState.viewBackgroundColor !== 'string' ||
    appState.viewBackgroundColor.toLowerCase() === LEGACY_DARK_CANVAS_COLOR
  ) {
    appState.viewBackgroundColor = DEFAULT_EXCALIDRAW_CANVAS_COLOR;
  }

  return {
    ...scene,
    elements: scene.elements || [],
    appState,
    files: scene.files || {},
    scrollToContent: scene.scrollToContent ?? true,
  };
}

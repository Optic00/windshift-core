import { mount } from 'svelte';
import './app.css';
import { registerMobileServiceWorker } from './lib/mobile/serviceWorkerClient.js';
import { installContextPathTranslation } from './lib/runtime/contextPath.js';
import { initCrossTabSync } from './lib/utils/crossTabSync.js';

installContextPathTranslation();
// Register before the large application chunk loads. This lets installed PWAs
// receive worker updates and navigation recovery even if startup later fails.
void registerMobileServiceWorker();

const { default: App } = await import('./App.svelte');

const target = document.getElementById('app');
// Keep the no-JavaScript/module-failure recovery visible until the root chunk
// is ready, then remove it before Svelte mounts. The placeholder is ordinary
// document flow, so it never overlays or intercepts the application in E2E.
target.replaceChildren();
const app = mount(App, {
  target,
});

// Refresh other open Windshift tabs when this tab mutates a work item.
// Handler injected (rather than imported by crossTabSync) to avoid an api ↔
// stores import cycle. initCrossTabSync is a no-op when BroadcastChannel is
// unavailable, so this is safe to run unconditionally.
initCrossTabSync({
  refreshCollectionDeltas: async () => {
    const { refreshCollectionDeltas } = await import('./lib/stores/collectionContext.js');
    return refreshCollectionDeltas();
  },
});

export default app;

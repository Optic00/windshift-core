import { mount } from 'svelte';
import './app.css';
import { installContextPathTranslation } from './lib/runtime/contextPath.js';
import { initCrossTabSync } from './lib/utils/crossTabSync.js';

installContextPathTranslation();

const { default: App } = await import('./App.svelte');

const app = mount(App, {
  target: document.getElementById('app'),
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

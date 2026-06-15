import { isTauri } from '../utils/isTauri.js';
import { DESKTOP_MODAL_FEATURES, DESKTOP_OPEN_MODAL_EVENT } from './events.js';

const supportedModals = new Set(DESKTOP_MODAL_FEATURES);

class DesktopBridge {
  modal = $state(null);
  initialized = false;
  unlisten = null;

  async init() {
    if (this.initialized || !isTauri()) return;
    this.initialized = true;

    try {
      const [{ listen }, { invoke }] = await Promise.all([
        import('@tauri-apps/api/event'),
        import('@tauri-apps/api/core'),
      ]);

      this.unlisten = await listen(DESKTOP_OPEN_MODAL_EVENT, (event) => {
        const modal = event?.payload?.modal;
        this.open(modal);
      });

      await invoke('set_webapp_ui_ready', { features: DESKTOP_MODAL_FEATURES });
    } catch (err) {
      console.warn('[desktop-bridge] init failed:', err);
    }
  }

  open(modal) {
    if (!supportedModals.has(modal)) {
      console.warn('[desktop-bridge] ignoring unsupported modal:', modal);
      return;
    }
    this.modal = modal;
  }

  close() {
    this.modal = null;
  }
}

export const desktopBridge = new DesktopBridge();

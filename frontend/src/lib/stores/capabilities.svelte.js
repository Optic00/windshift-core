/**
 * Store for tracking plugin capabilities.
 * Capabilities are loaded from /api/features and used to gate UI features.
 */

class CapabilitiesStore {
  /** @type {Set<string>} */
  capabilities = $state(new Set());
  logbookAvailable = $state(false);
  loaded = $state(false);

  hydrate(data) {
    if (!data) return;
    this.capabilities = new Set(data.capabilities || []);
    this.logbookAvailable = data.logbook_available === true;
    this.loaded = true;
  }

  /**
   * Load capabilities from the features endpoint.
   */
  async load() {
    try {
      const resp = await fetch('/api/features');
      if (resp.ok) {
        const data = await resp.json();
        this.hydrate(data);
      }
    } catch (err) {
      console.warn('Failed to load capabilities:', err);
    } finally {
      this.loaded = true;
    }
  }

  /**
   * Check if a capability is available.
   * @param {string} name
   * @returns {boolean}
   */
  has(name) {
    return this.capabilities.has(name);
  }
}

export const capabilitiesStore = new CapabilitiesStore();

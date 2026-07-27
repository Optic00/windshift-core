/** Reactive route-component import registry. Failed keys remain failed until
 * retry(), preventing effects from immediately repeating rejected imports. */
export class LazyComponentLoader {
  components = $state(new Map());
  loadingKeys = $state(new Set());
  errors = $state(new Map());

  /**
   * @param {Record<string, () => Promise<{ default: any }>>} loaders
   * @param {{ onError?: (key: string, error: unknown) => void }} [options]
   */
  constructor(loaders, { onError } = {}) {
    this.loaders = loaders;
    this.onError = onError;
  }

  async load(key) {
    const loader = this.loaders[key];
    if (!loader) return null;

    if (this.loadingKeys.has(key) || this.components.has(key) || this.errors.has(key)) {
      return this.components.get(key) ?? null;
    }

    this.loadingKeys = new Set(this.loadingKeys).add(key);

    try {
      const loadedModule = await loader();
      if (!loadedModule?.default) {
        throw new Error(`Lazy component "${key}" has no default export`);
      }

      this.components = new Map(this.components).set(key, loadedModule.default);
      return loadedModule.default;
    } catch (error) {
      this.errors = new Map(this.errors).set(key, error);
      this.onError?.(key, error);
      return null;
    } finally {
      const nextLoadingKeys = new Set(this.loadingKeys);
      nextLoadingKeys.delete(key);
      this.loadingKeys = nextLoadingKeys;
    }
  }

  retry(key) {
    if (this.loadingKeys.has(key)) return null;

    const nextErrors = new Map(this.errors);
    nextErrors.delete(key);
    this.errors = nextErrors;
    return this.load(key);
  }

  getComponent(key) {
    return this.components.get(key) ?? null;
  }

  isLoading(key) {
    return this.loadingKeys.has(key);
  }

  getError(key) {
    return this.errors.get(key) ?? null;
  }
}

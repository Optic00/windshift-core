/**
 * Creates reactive async data loader with loading and error states
 * @param {Function} fetchFn - Async function that fetches data
 * @returns {Object} Reactive loader with data, loading, error states and load method
 */
export function createAsyncLoader(fetchFn) {
  let data = $state([]);
  let loading = $state(false);
  let error = $state(null);
  let disposed = false;

  async function load() {
    if (loading || disposed) return;

    loading = true;
    error = null;

    try {
      const result = await fetchFn();
      if (disposed) return;
      data = result || [];
    } catch (e) {
      // Navigation can destroy the owner or abort its in-flight request.
      if (disposed || e?.name === 'AbortError') return;
      console.error('Failed to load data:', e);
      error = e.message || 'Failed to load data';
      data = [];
    } finally {
      loading = false;
    }
  }

  async function refetch() {
    data = [];
    await load();
  }

  function dispose() {
    disposed = true;
  }

  return {
    get data() {
      return data;
    },
    get loading() {
      return loading;
    },
    get error() {
      return error;
    },
    load,
    refetch,
    dispose,
  };
}

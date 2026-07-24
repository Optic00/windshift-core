/**
 * Creates reactive async data loader with loading and error states
 * @param {Function} fetchFn - Async function that fetches data
 * @returns {Object} Reactive loader with data, loading, error states and load method
 */
export function createAsyncLoader(fetchFn) {
  let data = $state([]);
  let loading = $state(false);
  let error = $state(null);

  async function load() {
    if (loading) return;

    loading = true;
    error = null;

    try {
      data = (await fetchFn()) || [];
    } catch (e) {
      // Navigation and superseded requests abort in-flight fetches by design.
      // The owning component is either gone or about to load newer data, so an
      // AbortError is neither a user-visible loader error nor a console error.
      if (e?.name === 'AbortError') return;
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
  };
}

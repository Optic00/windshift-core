// In-tab chat-completion bus: live views refetch immediately instead of waiting
// for polling. Server push, not cross-tab broadcast, handles other tabs.

const subscribers = new Set();

export const agentRuns = {
  emit() {
    for (const fn of subscribers) {
      try {
        fn();
      } catch (err) {
        console.error('agentRuns subscriber threw:', err);
      }
    }
  },
  subscribe(fn) {
    subscribers.add(fn);
    return () => subscribers.delete(fn);
  },
};

// Exposed for Playwright simulation; subscribers still refetch through auth.
if (typeof window !== 'undefined') {
  // eslint-disable-next-line no-underscore-dangle
  window.__agentRuns = agentRuns;
}

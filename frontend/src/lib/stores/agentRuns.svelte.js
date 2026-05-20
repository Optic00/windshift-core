// agentRuns is an in-memory pub/sub bus signalling "the AI chat agent
// just finished a run in this tab". chatStore emits after every successful
// /ai/chat response (whether or not any tool calls fired); live views
// (work item detail, board, action editor) subscribe and refetch their
// data so the user sees the agent's effects immediately instead of waiting
// for the 30s poller.
//
// No cross-tab broadcasting — server push is out of scope.

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

// Exposed on window so Playwright specs can simulate a chat completion
// without standing up an LLM. Benign in production: the bus is in-memory
// and each subscriber refetches via the authenticated API, which still
// enforces server-side permissions.
if (typeof window !== 'undefined') {
  // eslint-disable-next-line no-underscore-dangle
  window.__agentRuns = agentRuns;
}

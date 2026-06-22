import { usePoller } from './usePoller.svelte.js';

const ACTIVE_INTERVAL = 30_000;
const IDLE_INTERVAL = 5 * 60_000;

/**
 * Adaptive poller for work item fetching. Thin wrapper around usePoller
 * with the cadence board views have used since the pattern was introduced.
 *
 * @param {() => Promise<void>|void} fetchFn
 * @param {{ enabled?: () => boolean }} [opts] forwarded to usePoller (e.g. to
 *   demote polling while an SSE stream is healthy, WI-484).
 * @returns {{ poll: Function, isPolling: boolean, lastPollTime: number|null }}
 */
export function useWorkItemPoller(fetchFn, opts = {}) {
  return usePoller(fetchFn, { active: ACTIVE_INTERVAL, idle: IDLE_INTERVAL, ...opts });
}

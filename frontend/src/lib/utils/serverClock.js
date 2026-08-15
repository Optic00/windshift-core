/** Rolling server-clock offset from HTTP Date headers for consistent frontend now. */

const SAMPLE_COUNT = 5;
export const DRIFT_THRESHOLD_MS = 60_000;

/** @type {{id: number, at: number, serverTime: number, clientTime: number, offsetMs: number}[]} */
let samples = [];
let clockOffset = 0; // ms, positive = server ahead of client
let nextSampleId = 1;

/**
 * Record a new server-vs-client offset sample from an HTTP Date header.
 * Maintains a rolling median of the last SAMPLE_COUNT samples.
 * @param {string} serverDateHeader - RFC 1123 Date header value
 */
export function updateOffset(serverDateHeader) {
  if (!serverDateHeader) return;

  const serverTime = new Date(serverDateHeader).getTime();
  if (Number.isNaN(serverTime)) return;

  const clientTime = Date.now();
  samples.push({
    id: nextSampleId++,
    at: clientTime,
    serverTime,
    clientTime,
    offsetMs: serverTime - clientTime,
  });

  if (samples.length > SAMPLE_COUNT) {
    samples = samples.slice(-SAMPLE_COUNT);
  }

  // Median filters out one-off network jitter better than mean
  const sorted = samples.map((s) => s.offsetMs).sort((a, b) => a - b);
  const middle = Math.floor(sorted.length / 2);
  clockOffset =
    sorted.length % 2 === 0 ? (sorted[middle - 1] + sorted[middle]) / 2 : sorted[middle];
}

/**
 * Return a Date representing "now" on the server's clock.
 * Before any samples are collected this falls back to the client clock.
 * @returns {Date}
 */
export function serverNow() {
  return new Date(Date.now() + clockOffset);
}

/**
 * Raw offset in milliseconds (positive = server ahead).
 * @returns {number}
 */
export function getClockOffset() {
  return clockOffset;
}

/**
 * True when the absolute offset exceeds the drift threshold.
 * @returns {boolean}
 */
export function isClockDriftSignificant() {
  return Math.abs(clockOffset) > DRIFT_THRESHOLD_MS;
}

/**
 * Number of offset samples collected so far.
 * Useful for deciding when the estimate has stabilised.
 * @returns {number}
 */
export function getSampleCount() {
  return samples.length;
}

/**
 * Defensive copy of the rolling sample buffer (oldest first).
 * @returns {{id: number, at: number, serverTime: number, clientTime: number, offsetMs: number}[]}
 */
export function getSamples() {
  return samples.slice();
}

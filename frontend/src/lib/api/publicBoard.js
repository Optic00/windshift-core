/**
 * Public board API - uses plain fetch (no auth headers needed)
 */

/**
 * Fetch a public-board endpoint, throwing an error carrying the HTTP status
 * on non-2xx responses (shared by all public board calls).
 * @param {string} url
 * @returns {Promise<any>}
 */
async function publicBoardFetch(url) {
  const res = await fetch(url);
  if (!res.ok) {
    const err = new Error(`${res.status}`);
    /** @type {any} */ (err).status = res.status;
    throw err;
  }
  return res.json();
}

export const publicBoard = {
  get(slug) {
    return publicBoardFetch(`/api/public/board/${encodeURIComponent(slug)}`);
  },

  getItem(slug, key) {
    return publicBoardFetch(
      `/api/public/board/${encodeURIComponent(slug)}/items/${encodeURIComponent(key)}`
    );
  },
};

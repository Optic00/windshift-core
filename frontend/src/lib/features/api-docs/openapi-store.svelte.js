/** Pure reusable data helpers for the /api-docs renderer. */

/** @typedef {{ summary?: string, tags?: string[] }} OpenAPIOperation */
/** @typedef {{ paths?: Record<string, Record<string, OpenAPIOperation>> }} OpenAPISpec */
/** @typedef {{ tag: string, path: string, method: string, operation: OpenAPIOperation, id: string }} OperationEntry */
/** @typedef {{ tag: string, operations: OperationEntry[] }} OperationGroup */

const HTTP_METHODS = ['get', 'post', 'put', 'patch', 'delete', 'head', 'options'];

export const API_SPEC_VERSIONS = [
  { value: 'v2', label: 'API v2', url: '/api/v2/openapi.json' },
  { value: 'v1', label: 'API v1 (deprecated)', url: '/rest/api/v1/openapi.json' },
];

/**
 * Fetch the public embedded OpenAPI spec.
 * @param {string} url
 * @returns {Promise<OpenAPISpec>}
 */
export async function loadSpec(url = API_SPEC_VERSIONS[0].url) {
  const res = await fetch(url, { headers: { Accept: 'application/json' } });
  if (!res.ok) {
    throw new Error(`Failed to load OpenAPI spec: ${res.status} ${res.statusText}`);
  }
  return /** @type {Promise<OpenAPISpec>} */ (res.json());
}

/**
 * Resolve a local JSON Pointer or return null.
 * @param {unknown} spec
 * @param {string} ref
 */
export function resolveRef(spec, ref) {
  if (!ref || typeof ref !== 'string' || !ref.startsWith('#/')) return null;
  const segments = ref.slice(2).split('/');
  let cur = spec;
  for (const seg of segments) {
    if (cur == null) return null;
    if (typeof cur !== 'object') return null;
    const object = /** @type {Record<string, unknown>} */ (cur);
    cur = object[decodeURIComponent(seg).replace(/~1/g, '/').replace(/~0/g, '~')];
  }
  return cur ?? null;
}

/**
 * Group operations by first-seen tag, preserving path and method order.
 * @param {OpenAPISpec} spec
 * @returns {OperationGroup[]}
 */
export function groupOperationsByTag(spec) {
  if (!spec?.paths) return [];
  /** @type {string[]} */
  const tagOrder = [];
  /** @type {Map<string, OperationEntry[]>} */
  const byTag = new Map();
  /** @param {string} t */
  const seenTag = (t) => {
    if (!byTag.has(t)) {
      byTag.set(t, []);
      tagOrder.push(t);
    }
    return byTag.get(t) ?? [];
  };

  for (const [path, item] of Object.entries(spec.paths)) {
    for (const method of HTTP_METHODS) {
      const op = item[method];
      if (!op) continue;
      const tags = op.tags?.length ? op.tags : ['untagged'];
      const entry = {
        tag: tags[0],
        path,
        method,
        operation: op,
        id: operationId(method, path),
      };
      for (const tag of tags) {
        seenTag(tag).push(entry);
      }
    }
  }
  return tagOrder.map((tag) => ({ tag, operations: byTag.get(tag) ?? [] }));
}

/**
 * Stable id for an operation — used as the URL hash + scroll-target.
 * Mirrors the convention common in OpenAPI viewers: lowercase method,
 * path with slashes replaced by dashes, curly braces stripped.
 * @param {string} method
 * @param {string} path
 */
export function operationId(method, path) {
  const slug = path.replace(/[{}]/g, '').replace(/^\//, '').replace(/\//g, '-');
  return `op-${method.toLowerCase()}-${slug || 'root'}`;
}

/**
 * Filter the grouped operations by a free-text query against method, path,
 * tag, operation ID, and summary.
 * Empty groups are dropped.
 * @param {OperationGroup[]} groups
 * @param {string} query
 * @returns {OperationGroup[]}
 */
export function filterGroups(groups, query) {
  const q = (query || '').trim().toLowerCase();
  if (!q) return groups;
  return groups
    .map(({ tag, operations }) => ({
      tag,
      operations: operations.filter((entry) => {
        const haystack =
          `${tag} ${entry.method} ${entry.path} ${entry.id} ${entry.operation.summary || ''}`.toLowerCase();
        return haystack.includes(q);
      }),
    }))
    .filter((g) => g.operations.length > 0);
}

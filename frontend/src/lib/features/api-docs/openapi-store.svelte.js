/** Pure reusable data helpers for the /api-docs renderer. */

const HTTP_METHODS = ['get', 'post', 'put', 'patch', 'delete', 'head', 'options'];

/** Fetch the public embedded OpenAPI spec. */
export async function loadSpec(url = '/rest/api/v1/openapi.json') {
  const res = await fetch(url, { headers: { Accept: 'application/json' } });
  if (!res.ok) {
    throw new Error(`Failed to load OpenAPI spec: ${res.status} ${res.statusText}`);
  }
  return res.json();
}

/** Resolve a local JSON Pointer or return null. */
export function resolveRef(spec, ref) {
  if (!ref || typeof ref !== 'string' || !ref.startsWith('#/')) return null;
  const segments = ref.slice(2).split('/');
  let cur = spec;
  for (const seg of segments) {
    if (cur == null) return null;
    cur = cur[decodeURIComponent(seg).replace(/~1/g, '/').replace(/~0/g, '~')];
  }
  return cur ?? null;
}

/** Group operations by first-seen tag, preserving path and method order. */
export function groupOperationsByTag(spec) {
  if (!spec?.paths) return [];
  const tagOrder = [];
  const byTag = new Map();
  const seenTag = (t) => {
    if (!byTag.has(t)) {
      byTag.set(t, []);
      tagOrder.push(t);
    }
    return byTag.get(t);
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
  return tagOrder.map((tag) => ({ tag, operations: byTag.get(tag) }));
}

/**
 * Stable id for an operation — used as the URL hash + scroll-target.
 * Mirrors the convention common in OpenAPI viewers: lowercase method,
 * path with slashes replaced by dashes, curly braces stripped.
 */
export function operationId(method, path) {
  const slug = path.replace(/[{}]/g, '').replace(/^\//, '').replace(/\//g, '-');
  return `op-${method.toLowerCase()}-${slug || 'root'}`;
}

/**
 * Filter the grouped operations by a free-text query against path/summary.
 * Empty groups are dropped.
 */
export function filterGroups(groups, query) {
  const q = (query || '').trim().toLowerCase();
  if (!q) return groups;
  return groups
    .map(({ tag, operations }) => ({
      tag,
      operations: operations.filter((entry) => {
        const haystack =
          `${entry.method} ${entry.path} ${entry.operation.summary || ''}`.toLowerCase();
        return haystack.includes(q);
      }),
    }))
    .filter((g) => g.operations.length > 0);
}

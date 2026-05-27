/**
 * Data + helpers for the /api-docs renderer. Pure functions; no Svelte
 * components live here so it's easy to unit-test and reuse.
 */

const HTTP_METHODS = ['get', 'post', 'put', 'patch', 'delete', 'head', 'options'];

/**
 * Fetch the embedded OpenAPI spec from the running binary. Public route;
 * no auth required.
 */
export async function loadSpec(url = '/rest/api/v1/openapi.json') {
  const res = await fetch(url, { headers: { Accept: 'application/json' } });
  if (!res.ok) {
    throw new Error(`Failed to load OpenAPI spec: ${res.status} ${res.statusText}`);
  }
  return res.json();
}

/**
 * Resolve a JSON-Pointer like "#/components/schemas/Item" against the spec.
 * Returns the resolved object, or null if the ref is malformed or missing.
 */
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

/**
 * Group operations by tag. Returns an array of:
 *   { tag, operations: [{ tag, path, method, operation, id }, ...] }
 *
 * The order of tags is the order they first appear in the spec's paths so
 * that the rendered sidebar matches the structure of the original spec.
 * Operations within a tag stay in path order, then method order.
 */
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

# Authorization cache consistency

Windshift keeps permission snapshots, the active-workspace list, and workspace
key resolution in process-local memory. Authorization-affecting writes must
submit one `services.AuthorizationInvalidation` after their database transaction
commits.

The invalidation plan captures affected user IDs before rows are removed. It
also records whether the mutation changes implicit Everyone access, active
workspace membership, or workspace keys. Targeted permission invalidation
falls back to a full permission-cache reset when the affected users or their
owned agents cannot be invalidated completely. A cache refresh error is
returned to the mutation caller instead of reporting a clean success with a
known stale local cache.

Permission snapshot builds run outside the invalidation lock. Every build
captures the current generation and may commit only if that generation is
still current. A build that overlaps an invalidation is discarded and rebuilt,
so it cannot restore pre-mutation authorization.

These guarantees describe consistency within one running process. They do not
declare which database or deployment topologies are supported. Because the
caches are process-local, deployments with more than one application process
need a shared invalidation transport before the same immediate-consistency
guarantee can extend across processes.

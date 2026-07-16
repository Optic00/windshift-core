import { derived, writable } from 'svelte/store';
import { api } from '../api.js';

// Current workspace store - automatically syncs with route
function createCurrentWorkspaceStore() {
  const { subscribe, set, update } = writable(null);
  let lastWorkspaceId = null;
  let loadGeneration = 0;

  return {
    subscribe,

    // Patch workspace with partial updates (no API call)
    patch(updates) {
      update((ws) => (ws ? { ...ws, ...updates } : null));
    },

    // Load workspace by ID
    async load(workspaceId) {
      if (!workspaceId) {
        loadGeneration += 1;
        set(null);
        lastWorkspaceId = null;
        return;
      }

      // Avoid unnecessary API calls if workspace ID hasn't changed
      if (workspaceId === lastWorkspaceId) {
        return;
      }

      const generation = ++loadGeneration;
      try {
        const workspace = await api.workspaces.get(workspaceId);
        if (generation !== loadGeneration) return;
        set(workspace);
        // Only mark this id as loaded once the fetch actually succeeded —
        // otherwise a transient failure would suppress all retries.
        lastWorkspaceId = workspaceId;
      } catch (error) {
        if (generation !== loadGeneration) return;
        console.error('Failed to load workspace:', error);
        set(null);
        lastWorkspaceId = null;
      }
    },

    // Clear workspace
    clear() {
      loadGeneration += 1;
      set(null);
      lastWorkspaceId = null;
    },
  };
}

// Workspaces store - manages the list of all workspaces
function createWorkspacesStore() {
  const workspaces = writable([]);
  const personalWorkspace = writable(null);
  const loaded = writable(false);
  const loading = writable(false);
  let lifecycleGeneration = 0;
  let listLoadGeneration = 0;
  let personalLoadGeneration = 0;

  // Derived store for regular (non-personal) workspaces
  const regularWorkspaces = derived(workspaces, ($workspaces) =>
    $workspaces.filter((ws) => !ws.is_personal)
  );

  // Create a derived store that combines all state for easy subscription
  const combined = derived(
    [workspaces, personalWorkspace, loaded, loading, regularWorkspaces],
    ([$workspaces, $personalWorkspace, $loaded, $loading, $regularWorkspaces]) => ({
      workspaces: $workspaces,
      allWorkspaces: $workspaces,
      personalWorkspace: $personalWorkspace,
      loaded: $loaded,
      loading: $loading,
      regularWorkspaces: $regularWorkspaces,
    })
  );

  return {
    // Subscribe to combined state
    subscribe: combined.subscribe,

    // Load all workspaces (but not personal workspace - that's loaded on-demand)
    async load() {
      const lifecycle = lifecycleGeneration;
      const generation = ++listLoadGeneration;
      loading.set(true);

      try {
        const allWorkspaces = await api.workspaces.getAll();
        if (lifecycle !== lifecycleGeneration || generation !== listLoadGeneration) return;

        workspaces.set(allWorkspaces || []);
        // Don't set personalWorkspace here - it's loaded on-demand
        loaded.set(true);
      } catch (error) {
        if (lifecycle !== lifecycleGeneration || generation !== listLoadGeneration) return;
        console.error('Failed to load workspaces:', error);
        workspaces.set([]);
        loaded.set(true);
      } finally {
        if (lifecycle === lifecycleGeneration && generation === listLoadGeneration) {
          loading.set(false);
        }
      }
    },

    // Load personal workspace on-demand
    async loadPersonalWorkspace() {
      const lifecycle = lifecycleGeneration;
      const generation = ++personalLoadGeneration;
      try {
        const personal = await api.workspaces.getOrCreatePersonal();
        if (lifecycle !== lifecycleGeneration || generation !== personalLoadGeneration) {
          return null;
        }
        personalWorkspace.set(personal);
        return personal;
      } catch (error) {
        if (lifecycle !== lifecycleGeneration || generation !== personalLoadGeneration) {
          return null;
        }
        console.error('Failed to load personal workspace:', error);
        return null;
      }
    },

    // Force reload from API
    async reload() {
      loaded.set(false);
      loading.set(false);
      await this.load();
    },

    // Add a new workspace to the store
    add(workspace) {
      workspaces.update((ws) => [...ws, workspace]);
    },

    // Update an existing workspace in the store
    updateWorkspace(id, updates) {
      workspaces.update((ws) => ws.map((w) => (w.id === id ? { ...w, ...updates } : w)));
    },

    // Remove a workspace from the store
    remove(id) {
      workspaces.update((ws) => ws.filter((w) => w.id !== id));
    },

    // Clear the store
    clear() {
      lifecycleGeneration += 1;
      workspaces.set([]);
      personalWorkspace.set(null);
      loaded.set(false);
      loading.set(false);
    },
  };
}

export const currentWorkspace = createCurrentWorkspaceStore();
export const workspacesStore = createWorkspacesStore();

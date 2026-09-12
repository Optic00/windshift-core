<script>
  import { Server, Plus, ExternalLink, RefreshCw, Trash2, Search, Loader2, X } from '@lucide/svelte';
  import { netboxConnections, netboxLinks } from '../../api/netbox.js';
  import Button from '../../components/Button.svelte';
  import Input from '../../components/Input.svelte';
  import NativeSelect from '../../components/NativeSelect.svelte';
  import FormField from '../../components/FormField.svelte';
  import AlertBox from '../../components/AlertBox.svelte';
  import { t } from '../../stores/i18n.svelte.js';
  import { safeHref } from '../../utils/sanitize';

  let { itemId, workspaceId, canEdit = false } = $props();
  let connections = $state([]);
  let links = $state([]);
  let results = $state([]);
  let loading = $state(true);
  let searching = $state(false);
  let showPicker = $state(false);
  let error = $state('');
  let pickerError = $state('');
  let query = $state('');
  let selectedConnectionId = $state('');
  let objectType = $state('dcim.device');
  let offset = $state(0);
  let nextOffset = $state(null);
  let refreshingId = $state(null);
  let removingId = $state(null);
  let linkingKey = $state(null);
  let hasSearched = $state(false);
  let mutating = $derived(Boolean(refreshingId || removingId || linkingKey));
  let contextVersion = 0;
  let searchVersion = 0;
  let loadController;
  let searchController;

  $effect(() => {
    const id = itemId;
    const workspace = workspaceId;
    const version = ++contextVersion;
    reset();
    void load(id, workspace, version);
    return () => {
      contextVersion++;
      searchVersion++;
      loadController?.abort();
      searchController?.abort();
    };
  });

  function current(version, id = itemId, workspace = workspaceId) {
    return (
      version === contextVersion &&
      String(id) === String(itemId) &&
      String(workspace) === String(workspaceId)
    );
  }

  function reset() {
    loadController?.abort();
    searchController?.abort();
    searchVersion++;
    connections = [];
    links = [];
    results = [];
    loading = Boolean(itemId && workspaceId);
    searching = false;
    showPicker = false;
    error = '';
    pickerError = '';
    query = '';
    selectedConnectionId = '';
    offset = 0;
    nextOffset = null;
    hasSearched = false;
    refreshingId = null;
    removingId = null;
    linkingKey = null;
  }

  async function load(id = itemId, workspace = workspaceId, version = contextVersion) {
    loadController?.abort();
    const controller = new AbortController();
    loadController = controller;
    if (!id || !workspace) {
      loading = false;
      return;
    }
    loading = true;
    try {
      const [nextConnections, nextLinks] = await Promise.all([
        netboxConnections.forWorkspace(workspace, { signal: controller.signal }),
        netboxLinks.forItem(id, { signal: controller.signal }),
      ]);
      if (!current(version, id, workspace) || controller.signal.aborted) return;
      connections = nextConnections || [];
      links = nextLinks || [];
      if (!selectedConnectionId) selectedConnectionId = connections[0]?.id || '';
      error = '';
    } catch (err) {
      if (current(version, id, workspace) && err?.name !== 'AbortError') {
        error = t('netbox.loadLinksFailed');
      }
    } finally {
      if (current(version, id, workspace) && !controller.signal.aborted) loading = false;
    }
  }

  function invalidateSearch() {
    searchController?.abort();
    searchVersion++;
    searching = false;
    results = [];
    offset = 0;
    nextOffset = null;
    pickerError = '';
    hasSearched = false;
  }

  function openPicker() {
    showPicker = true;
    selectedConnectionId ||= connections[0]?.id || '';
    invalidateSearch();
  }

  async function search(atOffset = 0) {
    if (query.trim().length < 2 || !selectedConnectionId) {
      pickerError = t('netbox.searchHint');
      return;
    }
    searchController?.abort();
    const controller = new AbortController();
    searchController = controller;
    const version = ++searchVersion;
    const context = contextVersion;
    searching = true;
    pickerError = '';
    try {
      const page = await netboxConnections.search(
        workspaceId,
        selectedConnectionId,
        { object_type: objectType, q: query.trim(), limit: 20, offset: atOffset },
        { signal: controller.signal }
      );
      if (version !== searchVersion || context !== contextVersion || controller.signal.aborted) return;
      results = page.results || [];
      offset = atOffset;
      nextOffset = page.has_more ? page.next_offset : null;
      hasSearched = true;
    } catch (err) {
      if (version === searchVersion && context === contextVersion && err?.name !== 'AbortError') {
        pickerError = err?.message || t('netbox.loadLinksFailed');
      }
    } finally {
      if (version === searchVersion && context === contextVersion) searching = false;
    }
  }

  async function linkObject(object) {
    if (mutating) return;
    const context = contextVersion;
    const id = itemId;
    const key = object.external_id;
    linkingKey = key;
    pickerError = '';
    try {
      await netboxLinks.create(id, {
        connection_id: selectedConnectionId,
        object_type: object.object_type,
        object_id: object.object_id,
      });
      if (!current(context, id)) return;
      showPicker = false;
      invalidateSearch();
      await load(id, workspaceId, context);
    } catch (err) {
      if (current(context, id)) pickerError = err?.message || t('netbox.linkFailed');
    } finally {
      if (current(context, id)) linkingKey = null;
    }
  }

  async function refresh(link) {
    if (mutating) return;
    const context = contextVersion;
    const id = itemId;
    refreshingId = link.id;
    error = '';
    try {
      await netboxLinks.refresh(id, link.id);
      if (current(context, id)) await load(id, workspaceId, context);
    } catch {
      if (current(context, id)) error = t('netbox.refreshFailed');
    } finally {
      if (current(context, id)) refreshingId = null;
    }
  }

  async function unlink(link) {
    if (mutating) return;
    const context = contextVersion;
    const id = itemId;
    removingId = link.id;
    try {
      await netboxLinks.delete(id, link.id);
      if (current(context, id)) await load(id, workspaceId, context);
    } catch {
      if (current(context, id)) error = t('netbox.unlinkFailed');
    } finally {
      if (current(context, id)) removingId = null;
    }
  }

  function displayDate(value) {
    return value ? new Date(value).toLocaleString() : '';
  }
</script>

{#if loading || error || connections.length || links.length}
<section class="border-t pt-4 mt-4" style="border-color:var(--ds-border)" data-testid="netbox-item-panel">
  <div class="flex items-center justify-between gap-2 mb-3">
    <div class="flex items-center gap-2">
      <Server class="w-4 h-4" />
      <h3 class="font-semibold text-sm" style="color:var(--ds-text)">
        {t('netbox.panelTitle')}
      </h3>
    </div>
    {#if canEdit}
      <Button size="small" variant="ghost" icon={Plus} onclick={openPicker} disabled={!connections.length}>
        {t('netbox.linkObject')}
      </Button>
    {/if}
  </div>
  <div aria-live="polite">
    {#if error}<AlertBox message={error} class="mb-3" />{/if}
  </div>
  {#if loading}
    <div class="flex justify-center p-4"><Loader2 class="w-4 h-4 animate-spin" /></div>
  {:else if !links.length}
    <p class="text-sm" style="color:var(--ds-text-subtle)">
      {connections.length ? t('netbox.noLinks') : t('netbox.noAvailableConnections')}
    </p>
  {:else}
    <div class="space-y-3">
      {#each links as link (`${link.object.object_type}:${link.object.object_id}:${link.id}`)}
        <article
          class="rounded border p-3"
          style="border-color:var(--ds-border);background:var(--ds-surface-raised)"
        >
          <div class="flex items-start justify-between gap-2">
            <div class="font-medium text-sm break-words" style="color:var(--ds-text)">
              {link.object.name}
            </div>
            <a
              href={safeHref(link.object.url)}
              target="_blank"
              rel="noopener noreferrer"
              aria-label={t('netbox.open')}
              class="p-1 rounded focus:outline-none focus:ring-2 focus:ring-[var(--ds-border-focused)]"
            ><ExternalLink class="w-4 h-4" /></a>
          </div>
          <div
            class="text-xs mt-1 flex flex-wrap gap-x-3 gap-y-1"
            style="color:var(--ds-text-subtle)"
          >
            <span>{link.connection_name}</span>
            <span>{link.object.object_type === 'dcim.device' ? t('netbox.devices') : t('netbox.virtualMachines')}</span>
            {#if link.object.status}<span>{t('netbox.status')}: {link.object.status}</span>{/if}
            {#if link.object.site}<span>{t('netbox.site')}: {link.object.site}</span>{/if}
            {#if link.object.role}<span>{t('netbox.role')}: {link.object.role}</span>{/if}
            {#if link.object.primary_ip4}<span>{t('netbox.ipv4')}: {link.object.primary_ip4}</span>{/if}
            {#if link.object.primary_ip6}<span>{t('netbox.ipv6')}: {link.object.primary_ip6}</span>{/if}
          </div>
          <div class="mt-2 flex flex-wrap items-center justify-between gap-2">
            <span class="text-xs" style="color:var(--ds-text-subtle)">
              {t('netbox.snapshotFrom', { date: displayDate(link.snapshot_updated_at) })}
            </span>
            {#if canEdit}
              <div class="flex gap-1">
                <Button size="small" variant="ghost" icon={RefreshCw} loading={refreshingId === link.id} disabled={mutating} onclick={() => refresh(link)}>
                  {t('netbox.refresh')}
                </Button>
                <Button size="small" variant="danger-ghost" icon={Trash2} loading={removingId === link.id} disabled={mutating} onclick={() => unlink(link)}>
                  {t('netbox.unlink')}
                </Button>
              </div>
            {/if}
          </div>
        </article>
      {/each}
    </div>
  {/if}

  {#if showPicker && canEdit}
    <div
      class="mt-4 rounded border p-3"
      style="border-color:var(--ds-border);background:var(--ds-surface-raised)"
    >
      <div class="flex justify-between items-center mb-3">
        <h4 class="font-medium text-sm">{t('netbox.linkObject')}</h4>
        <Button size="small" variant="ghost" icon={X} title={t('netbox.close')} onclick={() => showPicker = false} />
      </div>
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-x-3">
        <FormField label={t('netbox.connection')}>
          <NativeSelect bind:value={selectedConnectionId} onchange={invalidateSearch} options={connections.map((c) => ({ value: c.id, label: c.name }))} />
        </FormField>
        <FormField label={t('netbox.type')}>
          <NativeSelect bind:value={objectType} onchange={invalidateSearch} options={[{ value: 'dcim.device', label: t('netbox.devices') }, { value: 'virtualization.virtualmachine', label: t('netbox.virtualMachines') }]} />
        </FormField>
      </div>
      <form
        class="flex flex-col sm:flex-row gap-2"
        onsubmit={(e) => {
          e.preventDefault();
          search(0);
        }}
      >
        <Input bind:value={query} minlength={2} maxlength={200} placeholder={t('netbox.searchPlaceholder')} oninput={invalidateSearch} />
        <Button type="submit" variant="primary" icon={Search} loading={searching} disabled={query.trim().length < 2}>
          {t('netbox.search')}
        </Button>
      </form>
      <div aria-live="polite" class="mt-3">
        {#if pickerError}
          <AlertBox message={pickerError} />
        {:else if !searching && hasSearched && results.length === 0}
          <p class="text-sm" style="color:var(--ds-text-subtle)">{t('netbox.noResults')}</p>
        {/if}
      </div>
      {#if results.length}
        <div class="mt-3 divide-y" style="border-color:var(--ds-border)">
          {#each results as object (`${object.object_type}:${object.object_id}`)}
            <div class="py-2 flex items-center justify-between gap-3">
              <div class="min-w-0">
                <div class="text-sm font-medium truncate">{object.name}</div>
                <div class="text-xs truncate" style="color:var(--ds-text-subtle)">
                  {[object.site, object.status].filter(Boolean).join(' · ')}
                </div>
              </div>
              <Button size="small" loading={linkingKey === object.external_id} disabled={mutating} onclick={() => linkObject(object)}>
                {t('netbox.link')}
              </Button>
            </div>
          {/each}
        </div>
        <div class="mt-3 flex justify-between">
          <Button size="small" variant="ghost" disabled={offset === 0} onclick={() => search(Math.max(0, offset - 20))}>
            {t('netbox.previous')}
          </Button>
          <Button size="small" variant="ghost" disabled={nextOffset == null} onclick={() => search(nextOffset)}>
            {t('netbox.next')}
          </Button>
        </div>
      {/if}
    </div>
  {/if}
</section>
{/if}

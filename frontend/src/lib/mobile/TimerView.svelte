<script>
  import { onMount } from 'svelte';
  import { Square, Clock, Plus, ExternalLink } from '@lucide/svelte';
  import { api } from '../api.js';
  import { navigate } from '../router.js';
  import { timerStore } from '../stores/timerStore.svelte.js';
  import { formatDate } from '../utils/dateFormatter.js';
  import MobileHeader from './MobileHeader.svelte';

  let worklogs = $state([]);
  let logsLoading = $state(false);
  let projects = $state([]);
  let showLog = $state(false);
  let saving = $state(false);
  let form = $state({ project_id: '', duration: '', description: '' });
  let version = 0;

  const activeTimer = $derived(timerStore.activeTimer);

  function fmtMinutes(min) {
    if (!min && min !== 0) return '';
    const h = Math.floor(min / 60);
    const m = Math.round(min % 60);
    return h > 0 ? `${h}h ${m}m` : `${m}m`;
  }

  function fmtDay(epochSeconds) {
    if (!epochSeconds) return '';
    return new Date(epochSeconds * 1000).toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
  }

  function workItemKey(w) {
    return w.workspace_key && w.workspace_item_number ? `${w.workspace_key}-${w.workspace_item_number}` : null;
  }

  async function loadWorklogs() {
    const v = ++version;
    logsLoading = true;
    try {
      const res = await api.time.worklogs.getAll({ limit: 20 });
      if (v !== version) return;
      const list = Array.isArray(res) ? res : (res?.items ?? res?.worklogs ?? []);
      worklogs = [...list].sort((a, b) => (b.date ?? 0) - (a.date ?? 0)).slice(0, 20);
    } catch (err) {
      console.error('Failed to load worklogs:', err);
      worklogs = [];
    } finally {
      if (v === version) logsLoading = false;
    }
  }

  async function loadProjects() {
    try {
      const res = await api.time.projects.getAll();
      projects = Array.isArray(res) ? res : (res?.items ?? res?.projects ?? []);
    } catch (err) {
      console.error('Failed to load projects:', err);
      projects = [];
    }
  }

  async function stopTimer() {
    try {
      await timerStore.stop();
      await loadWorklogs();
    } catch (err) {
      console.error('Failed to stop timer:', err);
    }
  }

  function openLog() {
    showLog = true;
    if (projects.length === 0) loadProjects();
  }

  async function submitLog() {
    const minutes = parseInt(form.duration, 10);
    if (!form.project_id || !form.description.trim() || !minutes) return;
    saving = true;
    try {
      await api.time.worklogs.create({
        project_id: parseInt(form.project_id, 10),
        description: form.description.trim(),
        date: formatDate(new Date()),
        duration_minutes: minutes,
      });
      form = { project_id: '', duration: '', description: '' };
      showLog = false;
      await loadWorklogs();
    } catch (err) {
      console.error('Failed to save worklog:', err);
    } finally {
      saving = false;
    }
  }

  onMount(() => {
    timerStore.initialize();
    loadWorklogs();
  });
</script>

<MobileHeader title="Timer" />

<div class="content">
  <!-- Active timer card -->
  <section class="timer-card" class:running={!!activeTimer} data-testid="timer-card">
    {#if activeTimer}
      <div class="t-top">
        <Clock size={18} />
        {#if workItemKey(activeTimer)}
          <a
            class="t-key"
            href={`/m/items/${activeTimer.item_id}`}
            data-testid="timer-item-link"
          >{workItemKey(activeTimer)} <ExternalLink size={12} /></a>
        {:else}
          <span class="t-key">{activeTimer.project_name ?? 'Running'}</span>
        {/if}
      </div>
      <div class="t-duration" data-testid="timer-duration">{timerStore.durationFormatted}</div>
      {#if activeTimer.item_title}
        <div class="t-title">{activeTimer.item_title}</div>
      {/if}
      <button class="btn-stop" onclick={stopTimer} disabled={timerStore.syncing} data-testid="timer-stop" type="button">
        <Square size={16} /> Stop
      </button>
    {:else}
      <div class="t-idle" data-testid="timer-idle">
        <Clock size={20} />
        <p>No timer running</p>
        <span>Start one from a work item's detail screen.</span>
      </div>
    {/if}
  </section>

  <!-- Quick log -->
  <section class="block">
    <div class="block-head">
      <h2>Recent worklogs</h2>
      <button class="btn-log" onclick={openLog} data-testid="quick-log-open" type="button">
        <Plus size={16} /> Quick log
      </button>
    </div>

    {#if showLog}
      <form class="log-form" onsubmit={(e) => { e.preventDefault(); submitLog(); }} data-testid="quick-log-form">
        <label>
          <span>Project</span>
          <select bind:value={form.project_id} data-testid="quick-log-project" required>
            <option value="" disabled>Select a project…</option>
            {#each projects as p (p.id)}
              <option value={p.id}>{p.name}</option>
            {/each}
          </select>
        </label>
        <label>
          <span>Minutes</span>
          <input type="number" min="1" bind:value={form.duration} data-testid="quick-log-minutes" placeholder="30" required />
        </label>
        <label>
          <span>Description</span>
          <input type="text" bind:value={form.description} data-testid="quick-log-description" placeholder="What did you work on?" required />
        </label>
        <div class="log-actions">
          <button type="button" class="btn-cancel" onclick={() => (showLog = false)}>Cancel</button>
          <button type="submit" class="btn-save" disabled={saving} data-testid="quick-log-save">{saving ? 'Saving…' : 'Save'}</button>
        </div>
      </form>
    {/if}

    {#if logsLoading && worklogs.length === 0}
      <p class="msg">Loading…</p>
    {:else if worklogs.length === 0}
      <p class="msg" data-testid="worklogs-empty">No worklogs yet.</p>
    {:else}
      <ul class="worklogs" data-testid="worklogs-list">
        {#each worklogs as w (w.id)}
          <li class="wl">
            <div class="wl-main">
              <span class="wl-desc">{w.description || w.project_name || 'Worklog'}</span>
              {#if workItemKey(w)}<span class="wl-key">{workItemKey(w)}</span>{/if}
            </div>
            <div class="wl-meta">
              <span class="wl-dur">{fmtMinutes(w.duration_minutes)}</span>
              <span class="wl-day">{fmtDay(w.date)}</span>
            </div>
          </li>
        {/each}
      </ul>
    {/if}
  </section>
</div>

<style>
  .content { padding: 0.75rem; display: flex; flex-direction: column; gap: 1rem; }

  .timer-card {
    border: 1px solid var(--ds-border);
    border-radius: var(--radius-xl, 12px);
    padding: 1.25rem;
    text-align: center;
    background-color: var(--ds-surface-card, var(--ds-surface-raised));
    box-shadow: var(--ds-shadow-raised);
  }
  .timer-card.running { border-color: var(--ds-interactive); }

  .t-top { display: flex; align-items: center; justify-content: center; gap: 0.5rem; color: var(--ds-text-subtle); }
  .t-key {
    display: inline-flex; align-items: center; gap: 4px;
    font-family: var(--font-mono, monospace); font-size: 0.8125rem;
    color: var(--ds-text-link, var(--ds-interactive)); text-decoration: none;
  }
  .t-duration { font-size: 2.5rem; font-weight: var(--font-bold, 700); font-variant-numeric: tabular-nums; margin: 0.5rem 0; color: var(--ds-text); }
  .t-title { color: var(--ds-text-subtle); font-size: 0.875rem; margin-bottom: 0.75rem; }

  .btn-stop {
    display: inline-flex; align-items: center; gap: 0.4rem;
    padding: 0.6rem 1.5rem; border: none; border-radius: var(--radius-lg, 8px);
    background-color: var(--ds-danger, #e5484d); color: #fff; font-weight: var(--font-semibold, 600); cursor: pointer;
  }
  .btn-stop:disabled { opacity: 0.6; }

  .t-idle { display: flex; flex-direction: column; align-items: center; gap: 0.25rem; color: var(--ds-text-subtle); }
  .t-idle p { margin: 0.25rem 0 0; font-weight: var(--font-medium, 500); color: var(--ds-text); }
  .t-idle span { font-size: 0.8125rem; }

  .block-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 0.5rem; }
  .block-head h2 { font-size: 0.9375rem; font-weight: var(--font-semibold, 600); color: var(--ds-text); margin: 0; }

  .btn-log {
    display: inline-flex; align-items: center; gap: 0.3rem;
    padding: 0.4rem 0.75rem; border: 1px solid var(--ds-border); border-radius: var(--radius-md, 6px);
    background-color: var(--ds-surface); color: var(--ds-text); font-size: 0.8125rem; cursor: pointer;
  }

  .log-form {
    display: flex; flex-direction: column; gap: 0.6rem;
    padding: 0.85rem; margin-bottom: 0.85rem;
    border: 1px solid var(--ds-border); border-radius: var(--radius-lg, 8px);
    background-color: var(--ds-surface-raised);
  }
  .log-form label { display: flex; flex-direction: column; gap: 0.25rem; font-size: 0.75rem; color: var(--ds-text-subtle); }
  .log-form select, .log-form input {
    padding: 0.55rem; border: 1px solid var(--ds-border); border-radius: var(--radius-md, 6px);
    background-color: var(--ds-background-input, var(--ds-surface)); color: var(--ds-text); font-size: 0.9375rem;
  }
  .log-actions { display: flex; justify-content: flex-end; gap: 0.5rem; }
  .btn-cancel { padding: 0.5rem 1rem; border: 1px solid var(--ds-border); border-radius: var(--radius-md, 6px); background: var(--ds-surface); color: var(--ds-text); cursor: pointer; }
  .btn-save { padding: 0.5rem 1.25rem; border: none; border-radius: var(--radius-md, 6px); background: var(--ds-interactive); color: var(--ds-text-inverse, #fff); font-weight: var(--font-semibold, 600); cursor: pointer; }
  .btn-save:disabled { opacity: 0.6; }

  .worklogs { list-style: none; margin: 0; padding: 0; border: 1px solid var(--ds-border); border-radius: var(--radius-lg, 8px); overflow: hidden; }
  .wl { display: flex; align-items: center; justify-content: space-between; gap: 0.75rem; padding: 0.7rem 0.85rem; }
  .wl:not(:last-child) { border-bottom: 1px solid var(--ds-border); }
  .wl-main { min-width: 0; display: flex; flex-direction: column; gap: 2px; }
  .wl-desc { font-size: 0.875rem; color: var(--ds-text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .wl-key { font-family: var(--font-mono, monospace); font-size: 0.6875rem; color: var(--ds-text-subtle); }
  .wl-meta { flex-shrink: 0; text-align: right; }
  .wl-dur { display: block; font-weight: var(--font-semibold, 600); font-size: 0.875rem; color: var(--ds-text); }
  .wl-day { font-size: 0.6875rem; color: var(--ds-text-subtle); }

  .msg { padding: 1.5rem; text-align: center; color: var(--ds-text-subtle); font-size: 0.875rem; }
</style>

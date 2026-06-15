<script>
  // Agent log tab (WI-260): the runs an agent executed for this work item,
  // with a live-tailing transcript of the selected run. Pure add-on — reads
  // only the agent_runs/agent_run_events surface, never item state.
  import { onMount, onDestroy } from 'svelte';
  import { Bot, RefreshCw, TriangleAlert } from '@lucide/svelte';
  import { agentRuns } from '../../api/agentRuns.js';
  import Lozenge from '../../components/Lozenge.svelte';
  import EmptyState from '../../components/EmptyState.svelte';
  import { formatDateTimeLocale } from '../../utils/dateFormatter.js';
  import { t } from '../../stores/i18n.svelte.js';

  let { itemId } = $props();

  const RUNS_POLL_MS = 10_000;
  const EVENTS_POLL_MS = 1_500;
  const TERMINAL = ['succeeded', 'failed', 'canceled', 'killed'];

  let runs = $state([]);
  let loading = $state(true);
  let selectedRunId = $state(null);
  let lines = $state([]);
  let liveToken = null; // invalidates the tail loop on switch/unmount
  let runsTimer = null;

  // Recovery-aware "needs human review" flag for the selected run, parsed from
  // its review_flagged event (emitted by the runner on unrecovered, high-signal
  // tool misuse). Null when the selected run is clean.
  let reviewFlag = $state(/** @type {null | { reasons: string[] }} */ (null));

  // Re-run button state. A run is enqueued, not started synchronously, so the
  // button stays disabled while any run is in flight (hasActiveRun) AND while a
  // trigger request is outstanding (rerunning) — together they prevent stacking.
  let rerunning = $state(false);
  let rerunError = $state('');

  const selectedRun = $derived(runs.find((r) => r.id === selectedRunId) || null);
  const hasActiveRun = $derived(runs.some((r) => !TERMINAL.includes(r.status)));

  function statusAppearance(status) {
    switch (status) {
      case 'succeeded': return 'success';
      case 'failed': case 'killed': return 'error';
      case 'running': return 'inprogress';
      case 'canceled': return 'default';
      default: return 'info'; // queued
    }
  }

  // Inspection-grade transcript: unlike the bindings test panel this KEEPS
  // lifecycle and warning events (queued/claimed/stall warnings are exactly
  // what you need when a run goes nowhere), and still drops streaming
  // `content` deltas that duplicate the final message.
  function eventText(ev) {
    let payload;
    try {
      payload = JSON.parse(ev.payload_json);
    } catch {
      return ev.payload_json; // non-JSON line, show as-is
    }
    if (ev.type === 'lifecycle') {
      const pool = payload.target_pool_id ? ` (pool ${payload.target_pool_id})` : '';
      const runner = payload.runner_name || payload.runner_id;
      return `· ${payload.phase || 'lifecycle'}${pool}${runner ? ` by ${runner}` : ''}`;
    }
    if (ev.type === 'warning') {
      return payload.message ? `⚠ ${payload.message}` : null;
    }
    if (ev.type === 'review_flagged') {
      const reasons = Array.isArray(payload.reasons) ? payload.reasons.join('; ') : '';
      return `⚠ Needs human review — ${reasons || 'unrecovered tool misuse'}`;
    }
    switch (payload.type) {
      case 'message':
        return typeof payload.text === 'string' ? payload.text : null;
      case 'content':
        return null; // streaming duplicate of the final message
      case 'tool_start':
        return payload.args?.cmd ? `$ ${payload.args.cmd}` : `→ ${payload.tool || 'tool'}`;
      case 'tool_done':
        return null;
      case 'error':
        return payload.message ? `⚠ ${payload.message}` : null;
      case 'starting':
      case 'session_idle':
        return null;
      default:
        return typeof (payload.text ?? payload.message ?? payload.line) === 'string'
          ? (payload.text ?? payload.message ?? payload.line)
          : null;
    }
  }

  async function loadRuns() {
    try {
      const fetched = await agentRuns.listForItem(itemId, { limit: 50 });
      runs = fetched || [];
      if (!selectedRunId && runs.length) selectRun(runs[0].id);
    } finally {
      loading = false;
    }
  }

  // Pull the review flag out of the raw events of the selected run, if any.
  function scanReviewFlag(events) {
    for (const ev of events) {
      if (ev.type !== 'review_flagged') continue;
      try {
        const p = JSON.parse(ev.payload_json);
        reviewFlag = { reasons: Array.isArray(p.reasons) ? p.reasons : [] };
      } catch {
        reviewFlag = { reasons: [] };
      }
    }
  }

  async function selectRun(runId) {
    selectedRunId = runId;
    lines = [];
    reviewFlag = null;
    const token = Symbol('agent-log');
    liveToken = token;
    let afterId = 0;
    while (liveToken === token) {
      let run;
      try {
        const events = await agentRuns.listEventsAfter(runId, afterId, 200);
        if (liveToken !== token) return;
        if (events?.length) {
          afterId = events[events.length - 1].id;
          scanReviewFlag(events);
          const fresh = events.map(eventText).filter(Boolean);
          if (fresh.length) lines = [...lines, ...fresh];
        }
        run = await agentRuns.get(runId);
        if (liveToken !== token) return;
        runs = runs.map((r) => (r.id === run.id ? { ...r, ...run } : r));
      } catch {
        return; // run vanished or request failed; stop tailing quietly
      }
      if (TERMINAL.includes(run.status)) {
        const tail = await agentRuns.listEventsAfter(runId, afterId, 200).catch(() => []);
        if (liveToken === token && tail?.length) {
          scanReviewFlag(tail);
          lines = [...lines, ...tail.map(eventText).filter(Boolean)];
        }
        return;
      }
      await new Promise((resolve) => setTimeout(resolve, EVENTS_POLL_MS));
    }
  }

  async function doRerun() {
    if (rerunning || hasActiveRun) return;
    rerunning = true;
    rerunError = '';
    try {
      await agentRuns.rerun(itemId);
      // The new run is queued, not started; refresh and jump to it so the
      // transcript tails the fresh run. hasActiveRun then keeps the button
      // disabled until it reaches a terminal state.
      await loadRuns();
      if (runs.length) selectRun(runs[0].id);
    } catch (e) {
      rerunError = e?.message || t('items.agentRerunFailed');
    } finally {
      rerunning = false;
    }
  }

  onMount(() => {
    loadRuns();
    runsTimer = setInterval(loadRuns, RUNS_POLL_MS);
  });
  onDestroy(() => {
    liveToken = null;
    if (runsTimer) clearInterval(runsTimer);
  });
</script>

{#if loading}
  <div class="p-4 text-center" style="color: var(--ds-text-subtle);">{t('common.loading')}</div>
{:else if runs.length === 0}
  <EmptyState icon={Bot} title={t('items.agentLogEmpty')} />
{:else}
  <div class="space-y-3" data-testid="item-agent-log">
    <!-- Header: run count + manual re-run -->
    <div class="flex items-center justify-between gap-2">
      <span class="text-xs" style="color: var(--ds-text-subtle);">
        {runs.length} {runs.length === 1 ? t('items.agentRunSingular') : t('items.agentRunPlural')}
      </span>
      <button
        type="button"
        class="inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded text-xs transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
        style="border: 1px solid var(--ds-border); color: var(--ds-text); background-color: transparent;"
        onclick={doRerun}
        disabled={rerunning || hasActiveRun}
        title={hasActiveRun ? t('items.agentRerunBusy') : t('items.agentRerunTitle')}
        data-testid="agent-rerun-button"
      >
        <RefreshCw class={`w-3 h-3 ${rerunning ? 'animate-spin' : ''}`} />
        {rerunning ? t('items.agentRerunStarting') : hasActiveRun ? t('items.agentRerunBusy') : t('items.agentRerunLabel')}
      </button>
    </div>

    {#if rerunError}
      <div class="text-xs px-3 py-2 rounded" style="color: var(--ds-text-danger); border: 1px solid var(--ds-border-danger, #f87171); background-color: var(--ds-background-danger-subtle, #fef2f2);" data-testid="agent-rerun-error">
        {rerunError}
      </div>
    {/if}

    {#if reviewFlag}
      <div
        class="flex gap-2 px-3 py-2 rounded text-xs"
        style="color: var(--ds-text-danger, #b91c1c); border: 1px solid var(--ds-border-danger, #f87171); background-color: var(--ds-background-danger-subtle, #fef2f2);"
        role="status"
        data-testid="agent-review-flag"
      >
        <TriangleAlert class="w-4 h-4 shrink-0" />
        <div class="flex flex-col gap-0.5">
          <strong>{t('items.agentReviewFlagTitle')}</strong>
          <span>{t('items.agentReviewFlagBody')}</span>
          {#if reviewFlag.reasons.length}
            <ul class="list-disc pl-4 mt-0.5">
              {#each reviewFlag.reasons as reason}
                <li>{reason}</li>
              {/each}
            </ul>
          {/if}
        </div>
      </div>
    {/if}

    <!-- Run selector -->
    <div class="flex flex-wrap gap-2">
      {#each runs as run (run.id)}
        <button
          type="button"
          class="flex items-center gap-2 px-2.5 py-1.5 rounded text-xs transition-colors"
          style="
            border: 1px solid {selectedRunId === run.id ? 'var(--ds-border-focused)' : 'var(--ds-border)'};
            background-color: {selectedRunId === run.id ? 'var(--ds-background-selected)' : 'transparent'};
            color: var(--ds-text);
          "
          data-testid={`agent-log-run-${run.id}`}
          onclick={() => selectRun(run.id)}
        >
          <span>#{run.id}</span>
          <Lozenge appearance={statusAppearance(run.status)} size="sm">{run.status}</Lozenge>
          <span style="color: var(--ds-text-subtle);">{formatDateTimeLocale(run.queued_at)}</span>
        </button>
      {/each}
    </div>

    {#if selectedRun}
      {#if selectedRun.error}
        <div class="text-xs px-3 py-2 rounded" style="color: var(--ds-text-danger); border: 1px solid var(--ds-border);">
          {selectedRun.error}
        </div>
      {/if}
      <pre
        class="text-xs p-3 rounded overflow-auto whitespace-pre-wrap"
        style="background-color: var(--ds-background-input); border: 1px solid var(--ds-border); color: var(--ds-text); max-height: 420px;"
        data-testid="agent-log-transcript"
      >{lines.length ? lines.join('\n') : t('items.agentLogWaiting')}</pre>
      {#if !TERMINAL.includes(selectedRun.status)}
        <div class="flex items-center gap-1.5 text-xs" style="color: var(--ds-text-subtle);">
          <RefreshCw class="w-3 h-3 animate-spin" />
          {selectedRun.status}…
        </div>
      {/if}
    {/if}
  </div>
{/if}

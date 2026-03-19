<script>
  import { onMount } from 'svelte';
  import { Sparkles } from 'lucide-svelte';
  import Card from '../../components/Card.svelte';
  import MilkdownEditor from '../../editors/LazyMilkdownEditor.svelte';
  import { ai } from '../../api/ai.js';
  import { navigate } from '../../router.js';

  let briefing = $state(null);
  let loading = $state(true);
  let hidden = $state(false);
  let itemKeyMap = $state({});

  onMount(async () => {
    try {
      const data = await ai.dailyBriefing();
      console.log('[DailyBriefing] API response:', JSON.stringify(data));
      if (data && data.content) {
        if (data.references) {
          itemKeyMap = data.references;
        }
        briefing = data;
        console.log('[DailyBriefing] showing briefing, content length:', data.content.length);
      } else {
        hidden = true;
        console.log('[DailyBriefing] hiding: empty or missing content');
      }
    } catch (err) {
      // 503 or any error: AI not configured, hide component
      hidden = true;
      console.log('[DailyBriefing] hiding due to error:', err);
    } finally {
      loading = false;
    }
  });

  function preprocessBriefingContent(text) {
    if (!text) return '';
    // Unwrap bracketed references like [CSUC-6] → CSUC-6 (but not [CSUC-6](url))
    let result = text.replace(/\[([A-Z]{2,10}-\d+)\](?!\()/g, '$1');
    // Convert known item keys to markdown links
    result = result.replace(/\b([A-Z]{2,10}-\d+)\b/g, (_, key) => {
      return itemKeyMap[key] ? `[${key}](#)` : key;
    });
    return result;
  }

  function handleBriefingClick(e) {
    const link = e.target.closest('a');
    if (!link) return;
    const key = link.textContent.trim();
    if (!/^[A-Z]{2,10}-\d+$/.test(key)) return;
    e.preventDefault();
    const item = itemKeyMap[key];
    if (item) {
      navigate(`/workspaces/${item.workspace_id}/items/${item.item_id}`);
    }
  }
</script>

{#if !hidden}
  {#if loading}
    <div class="mb-6">
      <Card shadow padding="spacious">
        <div class="animate-pulse space-y-3">
          <div class="flex items-center gap-2">
            <div class="w-5 h-5 rounded" style="background-color: var(--ds-background-neutral);"></div>
            <div class="h-4 w-32 rounded" style="background-color: var(--ds-background-neutral);"></div>
          </div>
          <div class="h-3 w-full rounded" style="background-color: var(--ds-background-neutral);"></div>
          <div class="h-3 w-3/4 rounded" style="background-color: var(--ds-background-neutral);"></div>
          <div class="h-3 w-1/2 rounded" style="background-color: var(--ds-background-neutral);"></div>
        </div>
      </Card>
    </div>
  {:else if briefing}
    <div class="mb-6">
      <Card shadow padding="spacious">
        <div class="flex items-center justify-between mb-3">
          <div class="flex items-center gap-2">
            <Sparkles class="w-4 h-4" style="color: var(--ds-icon-accent);" />
            <h3 class="text-base font-semibold" style="color: var(--ds-text);">Daily Briefing</h3>
          </div>
          {#if briefing.generated_at}
            <span class="text-xs" style="color: var(--ds-text-subtle);">
              {new Date(briefing.generated_at).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })}
            </span>
          {/if}
        </div>
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div class="briefing-content max-w-none" style="color: var(--ds-text);" onclick={handleBriefingClick}>
          <MilkdownEditor content={preprocessBriefingContent(briefing.content)} readonly={true} showToolbar={false} compact={true} />
        </div>
      </Card>
    </div>
  {/if}
{/if}

<style>
  .briefing-content :global(.milkdown-editor .ProseMirror h1) { font-size: 0.8125rem; font-weight: 600; margin: 0.75rem 0 0.25rem; }
  .briefing-content :global(.milkdown-editor .ProseMirror h2) { font-size: 0.8125rem; font-weight: 600; margin: 0.75rem 0 0.25rem; }
  .briefing-content :global(.milkdown-editor .ProseMirror h3) { font-size: 0.75rem; font-weight: 600; margin: 0.5rem 0 0.25rem; }
  .briefing-content :global(.milkdown-editor .ProseMirror p) { font-size: 0.75rem; line-height: 1.5; margin: 0.25rem 0; }
  .briefing-content :global(.milkdown-editor .ProseMirror li) { font-size: 0.75rem; line-height: 1.5; }
  .briefing-content :global(.milkdown-editor .ProseMirror ul),
  .briefing-content :global(.milkdown-editor .ProseMirror ol) { font-size: 0.75rem; line-height: 1.5; margin: 0.25rem 0; padding-left: 1.25rem; }
  .briefing-content :global(strong) { font-weight: 600; }
  .briefing-content :global(a) {
    color: var(--ds-link);
    text-decoration: underline;
    cursor: pointer;
  }
  .briefing-content :global(a:hover) {
    text-decoration-thickness: 2px;
  }
</style>

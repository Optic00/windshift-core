<script>
  import { ArrowLeft, Calendar, ChevronRight, List, MessageSquare, Tag } from '@lucide/svelte';
  import Spinner from '../components/Spinner.svelte';
  import StatusBadge from '../components/StatusBadge.svelte';
  import Textarea from '../components/Textarea.svelte';
  import Button from '../components/Button.svelte';
  import PageHeader from '../layout/PageHeader.svelte';
  import { portalStore } from '../stores/portal.svelte.js';
  import { formatDateSimple, formatDateTimeLocale } from '../utils/dateFormatter.js';
</script>

{#if portalStore.selectedRequest}
  <div class="max-w-4xl">
    <button
      type="button"
      onclick={() => portalStore.closeRequestDetail()}
      class="inline-flex items-center gap-2 text-sm font-medium mb-6 hover:underline"
      style="color: var(--ds-text-link);"
    >
      <ArrowLeft class="w-4 h-4" />
      Back to requests
    </button>

    <header class="pb-7 border-b" style="border-color: var(--ds-border);">
      <div class="flex flex-wrap items-center gap-2 mb-3">
        <span class="text-sm font-mono" style="color: var(--ds-text-subtle);">
          {portalStore.selectedRequest.workspace_key}-{portalStore.selectedRequest.workspace_item_number}
        </span>
        <StatusBadge status={{ label: portalStore.selectedRequest.status, categoryColor: portalStore.selectedRequest.status_category_color }} />
      </div>
      <h1 class="text-2xl sm:text-3xl font-semibold tracking-tight" style="color: var(--ds-text);">
        {portalStore.selectedRequest.title}
      </h1>
      {#if portalStore.selectedRequest.description}
        <p class="text-base mt-3 leading-relaxed max-w-3xl" style="color: var(--ds-text-subtle);">
          {portalStore.selectedRequest.description}
        </p>
      {/if}
      <div class="flex flex-wrap gap-x-5 gap-y-2 mt-5 text-sm" style="color: var(--ds-text-subtle);">
        <div class="flex items-center gap-1.5">
          <Calendar class="w-4 h-4" />
          Created {formatDateSimple(portalStore.selectedRequest.created_at)}
        </div>
        {#if portalStore.selectedRequest.request_type_name}
          <div class="flex items-center gap-1.5">
            <Tag class="w-4 h-4" />
            {portalStore.selectedRequest.request_type_name}
          </div>
        {/if}
      </div>
    </header>

    <section class="pt-7 max-w-3xl">
      <h2 class="text-lg font-semibold mb-5" style="color: var(--ds-text);">Activity</h2>

      {#if portalStore.loadingComments}
        <div class="flex justify-center py-8">
          <Spinner />
        </div>
      {:else}
        <div class="space-y-5 mb-7">
          {#each portalStore.requestComments as comment}
            <article class="pl-4 border-l-2" style="border-color: var(--ds-border);">
              <div class="flex flex-wrap items-center justify-between gap-2 mb-1.5">
                <div class="font-medium text-sm" style="color: var(--ds-text);">
                  {comment.author_name}
                </div>
                <time class="text-xs" style="color: var(--ds-text-subtle);">
                  {formatDateTimeLocale(comment.created_at)}
                </time>
              </div>
              <p class="text-sm leading-relaxed" style="color: var(--ds-text);">{comment.content}</p>
            </article>
          {:else}
            <p class="text-sm py-2" style="color: var(--ds-text-subtle);">No comments yet.</p>
          {/each}
        </div>

        <div class="pt-5 border-t" style="border-color: var(--ds-border);">
          <label class="block text-sm font-medium mb-2" style="color: var(--ds-text);" for="portal-request-comment">
            Add a comment
          </label>
          <Textarea
            id="portal-request-comment"
            value={portalStore.newCommentContent}
            oninput={(event) => (portalStore.newCommentContent = event.target.value)}
            placeholder="Write an update or question…"
            rows={3}
          />
          <div class="flex justify-end mt-3">
            <!-- shortcut-guard-exempt: portal comments are an explicit, form-scoped submit action. -->
            <Button
              variant="primary"
              onclick={() => portalStore.addComment()}
              disabled={!portalStore.newCommentContent.trim() || portalStore.addingComment}
              loading={portalStore.addingComment}
            >
              Add comment
            </Button>
          </div>
        </div>
      {/if}
    </section>
  </div>
{:else}
  <div>
    <PageHeader
      title="My requests"
      subtitle="Track requests and continue conversations with the team."
      count={!portalStore.loadingRequests && portalStore.myRequests.length > 0
        ? portalStore.myRequests.length
        : null}
    />

    {#if portalStore.loadingRequests}
      <div class="flex justify-center py-12"><Spinner size="lg" /></div>
    {:else if portalStore.myRequests.length === 0}
      <div class="max-w-xl py-8 border-t" style="border-color: var(--ds-border);">
        <div class="flex items-start gap-3">
          <List class="w-5 h-5 mt-0.5" style="color: var(--ds-text-subtle);" />
          <div>
            <h2 class="text-base font-medium" style="color: var(--ds-text);">No requests yet</h2>
            <p class="text-sm mt-1" style="color: var(--ds-text-subtle);">
              Requests submitted through this portal will appear here.
            </p>
          </div>
        </div>
      </div>
    {:else}
      <div class="border rounded-md overflow-hidden" style="border-color: var(--ds-border); background-color: var(--ds-surface-card);">
        {#each portalStore.myRequests as request}
          <button
            data-testid="portal-my-requests-row"
            data-request-id={request.id}
            onclick={() => portalStore.viewRequest(request)}
            class="group w-full px-4 sm:px-5 py-4 border-b last:border-b-0 text-left transition-colors hover:bg-black/[0.025]"
            style="border-color: var(--ds-border);{request.status_is_completed ? ' opacity: 0.7;' : ''}"
          >
            <div class="flex items-start gap-4">
              <div class="min-w-0 flex-1">
                <div class="flex flex-wrap items-center gap-2 mb-1.5">
                  <span class="text-xs font-mono" style="color: var(--ds-text-subtle);">
                    {request.workspace_key}-{request.workspace_item_number}
                  </span>
                  <StatusBadge status={{ label: request.status, categoryColor: request.status_category_color }} />
                </div>
                <h2 class="font-semibold" style="color: var(--ds-text);">{request.title}</h2>
                {#if request.description}
                  <p class="text-sm mt-1 line-clamp-2" style="color: var(--ds-text-subtle);">
                    {request.description}
                  </p>
                {/if}
                <div class="flex flex-wrap items-center gap-x-4 gap-y-1 mt-3 text-xs" style="color: var(--ds-text-subtle);">
                  <span class="flex items-center gap-1"><Calendar class="w-3.5 h-3.5" />{formatDateSimple(request.created_at)}</span>
                  {#if request.comment_count > 0}
                    <span class="flex items-center gap-1"><MessageSquare class="w-3.5 h-3.5" />{request.comment_count}</span>
                  {/if}
                  {#if request.request_type_name}
                    <span class="flex items-center gap-1"><Tag class="w-3.5 h-3.5" />{request.request_type_name}</span>
                  {/if}
                </div>
              </div>
              <ChevronRight class="w-4 h-4 mt-2 flex-none" style="color: var(--ds-text-subtle);" />
            </div>
          </button>
        {/each}
      </div>
    {/if}
  </div>
{/if}

<style>
  .line-clamp-2 {
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
</style>

<script>
  import { useResizeObserver } from 'runed';
  import { ArrowLeft, ArrowRight, FileText, LockKeyhole } from '@lucide/svelte';
  import { currentRoute, navigate } from '../../router.js';
  import { authStore } from '../../stores';
  import Spinner from '../../components/Spinner.svelte';
  import FormRenderer from './FormRenderer.svelte';
  import { loadPublicFormBootstrap } from './publicFormData.js';

  let slug = $derived($currentRoute.params?.slug || '');
  let requestedFormId = $derived(parseRequestedFormId($currentRoute.params?.formId));
  let embed = $derived(new URLSearchParams(window.location.search).get('embed') === 'true');

  let channel = $state(null);
  let forms = $state([]);
  let selectedFormId = $state(null);
  let selectedFormDetail = $state(null);
  let loading = $state(true);
  let error = $state(null);
  let routeLoadVersion = 0;

  let isDarkMode = $derived(channel?.theme === 'dark' || (channel?.theme === 'auto' && window.matchMedia?.('(prefers-color-scheme: dark)').matches));
  let brandColor = $derived(channel?.brand_color || '#14b8a6');
  let logoUrl = $derived(channel?.logo_url || '');

  // The router keeps this root component mounted while switching between the
  // channel list and individual form URLs. Treat route params as authoritative
  // so browser Back/Forward follows the URL instead of stale local selection.
  $effect(() => {
    const routeSlug = slug;
    const routeFormId = requestedFormId;
    void loadFormChannel(routeSlug, routeFormId);
  });

  // Tell the parent window (widget.js iframe host) how tall we are so it can
  // resize the iframe. Runs only when embedded inside another window.
  function postHeight() {
    const height = document.documentElement.scrollHeight;
    window.parent.postMessage({ type: 'ws-form-resize', height }, '*');
  }
  $effect(() => {
    if (!embed || window.parent === window) return;
    postHeight();
  });
  useResizeObserver(
    () => (embed && window.parent !== window ? document.documentElement : null),
    postHeight
  );

  function parseRequestedFormId(rawFormId) {
    if (rawFormId === undefined) return null;
    if (!/^[1-9]\d*$/.test(rawFormId)) return NaN;
    const parsed = Number(rawFormId);
    return Number.isSafeInteger(parsed) ? parsed : NaN;
  }

  async function loadFormChannel(routeSlug, routeFormId) {
    const version = ++routeLoadVersion;
    try {
      loading = true;
      error = null;

      const bootstrap = await loadPublicFormBootstrap(routeSlug);
      if (version !== routeLoadVersion) return;

      channel = bootstrap.channel;
      forms = bootstrap.forms || [];
      selectedFormId = null;
      selectedFormDetail = null;

      if (routeFormId !== null) {
        if (!Number.isSafeInteger(routeFormId) || !forms.some(form => form.id === routeFormId)) {
          throw new Error('Form not found');
        }
        selectedFormId = routeFormId;
        if (bootstrap.form_detail?.form_id === routeFormId) {
          selectedFormDetail = bootstrap.form_detail;
        }
      } else if (forms.length === 1) {
        // Keep the channel URL canonical for sole-form channels.
        selectedFormId = forms[0].id;
        selectedFormDetail = bootstrap.form_detail || null;
      }
    } catch (err) {
      if (version !== routeLoadVersion) return;
      console.error('Failed to load form channel:', err);
      error = err.message || 'Form not found';
    } finally {
      if (version === routeLoadVersion) loading = false;
    }
  }

  function selectForm(formId) {
    selectedFormId = formId;
    selectedFormDetail = null;
    navigate(`/forms/${encodeURIComponent(slug)}/${formId}${embed ? '?embed=true' : ''}`);
  }

  function backToList() {
    selectedFormId = null;
    selectedFormDetail = null;
    navigate(`/forms/${encodeURIComponent(slug)}${embed ? '?embed=true' : ''}`);
  }
</script>

<div
  class="min-h-screen flex flex-col"
  style="background:
    radial-gradient(circle at top, color-mix(in srgb, {brandColor} 12%, transparent), transparent 34rem),
    {isDarkMode ? '#0f172a' : '#f8fafc'};"
  data-testid="public-form-page"
  data-ready={!loading && !error}
>
  {#if loading}
    <div class="flex-1 flex items-center justify-center">
      <Spinner />
    </div>
  {:else if error}
    <div class="flex-1 flex items-center justify-center">
      <div class="text-center">
        <div class="text-6xl mb-4">404</div>
        <p style="color: {isDarkMode ? '#94a3b8' : '#6b7280'};">{error}</p>
      </div>
    </div>
  {:else}
    <!-- Header (hidden in embed mode) -->
    {#if !embed}
      <header
        class="border-b"
        style="background-color: {isDarkMode ? '#1e293b' : '#ffffff'}; border-color: {isDarkMode ? '#334155' : '#e2e8f0'};"
      >
        <div class="max-w-3xl mx-auto px-6 py-4 flex items-center gap-4">
          {#if logoUrl}
            <img src={logoUrl} alt="" class="h-9 w-auto max-w-40 object-contain" />
          {:else}
            <div class="flex h-9 w-9 items-center justify-center rounded-lg" style="background: color-mix(in srgb, {brandColor} 14%, transparent); color: {brandColor};">
              <FileText class="h-5 w-5" />
            </div>
          {/if}
          <div>
            <h1 class="text-lg font-semibold" style="color: {isDarkMode ? '#f1f5f9' : '#0f172a'};">
              {channel?.name || 'Forms'}
            </h1>
            <p class="text-xs" style="color: {isDarkMode ? '#94a3b8' : '#64748b'};">
              Secure forms powered by Windshift
            </p>
          </div>
        </div>
      </header>
    {/if}

    <!-- Content -->
    <main class="flex-1 flex items-start justify-center {embed ? 'p-4' : 'px-6 py-10'}">
      <div class="w-full max-w-3xl">
        {#if selectedFormId}
          <!-- Show form -->
          {#if forms.length > 1 && !embed}
            <button
              onclick={backToList}
              data-testid="public-form-back"
              class="mb-4 text-sm font-medium flex items-center gap-1 transition-colors"
              style="color: {brandColor};"
            >
              <ArrowLeft class="h-4 w-4" />
              Back to forms
            </button>
          {/if}

          {@const selectedForm = forms.find(f => f.id === selectedFormId)}
          <div
            class="rounded-xl border p-6 {embed ? '' : 'shadow-sm'}"
            style="background-color: {isDarkMode ? '#1e293b' : '#ffffff'}; border-color: {isDarkMode ? '#334155' : '#e2e8f0'};"
          >
            {#if selectedForm}
              <div class="mb-6">
                <h2
                  data-testid="public-form-title"
                  class="text-xl font-bold"
                  style="color: {isDarkMode ? '#f1f5f9' : '#0f172a'};"
                >
                  {selectedForm.name}
                </h2>
                {#if selectedForm.description}
                  <p class="text-sm mt-1" style="color: {isDarkMode ? '#94a3b8' : '#6b7280'};">
                    {selectedForm.description}
                  </p>
                {/if}
              </div>
            {/if}

            <FormRenderer
              formSlug={slug}
              formId={selectedFormId}
              formConfig={selectedForm?.config}
              attachmentConfig={selectedForm?.config?.allow_attachments ? channel?.attachments : null}
              initialDetail={selectedFormDetail}
              authenticationRequired={selectedForm?.config?.require_auth === true && !$authStore.isAuthenticated}
              {embed}
              {brandColor}
              {isDarkMode}
            />
          </div>
        {:else if forms.length === 0}
          <!-- No forms -->
          <div class="rounded-2xl border p-10 text-center shadow-sm" style="background-color: {isDarkMode ? '#1e293b' : '#ffffff'}; border-color: {isDarkMode ? '#334155' : '#e2e8f0'};">
            <div class="w-16 h-16 mx-auto mb-4 rounded-full flex items-center justify-center" style="background-color: {brandColor}20;">
              <svg class="w-8 h-8" style="color: {brandColor};" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <path d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z" stroke-linecap="round" stroke-linejoin="round"/>
              </svg>
            </div>
            <h2 class="font-semibold" style="color: {isDarkMode ? '#f1f5f9' : '#0f172a'};">No forms available</h2>
            <p class="mt-1 text-sm" style="color: {isDarkMode ? '#94a3b8' : '#6b7280'};">This form channel has not published any forms yet.</p>
          </div>
        {:else}
          <!-- Form list -->
          <div class="mb-7 text-center">
            <p class="text-xs font-semibold uppercase tracking-[0.18em]" style="color: {brandColor};">How can we help?</p>
            <h2 class="mt-2 text-2xl font-bold" style="color: {isDarkMode ? '#f8fafc' : '#0f172a'};">Choose a form to get started</h2>
            <p class="mt-2 text-sm" style="color: {isDarkMode ? '#94a3b8' : '#64748b'};">Select the option that best matches what you need.</p>
          </div>
          <div class="grid gap-4 sm:grid-cols-2">
            {#each forms as form}
              <button
                onclick={() => selectForm(form.id)}
                data-testid={`public-form-option-${form.id}`}
                class="group w-full text-left p-5 rounded-2xl border transition-all hover:-translate-y-0.5 hover:shadow-lg"
                style="background-color: {isDarkMode ? '#1e293b' : '#ffffff'}; border-color: {isDarkMode ? '#334155' : '#e2e8f0'};"
              >
                <div class="flex h-full items-start gap-4">
                  <div class="w-10 h-10 rounded-lg flex items-center justify-center flex-shrink-0" style="background-color: {form.color || brandColor}20;">
                    <FileText class="h-5 w-5" style="color: {form.color || brandColor};" />
                  </div>
                  <div class="flex min-w-0 flex-1 flex-col self-stretch">
                    <div class="font-medium" style="color: {isDarkMode ? '#f1f5f9' : '#0f172a'};">
                      {form.name}
                    </div>
                    {#if form.description}
                      <div class="mt-1 line-clamp-2 text-sm" style="color: {isDarkMode ? '#94a3b8' : '#6b7280'};">
                        {form.description}
                      </div>
                    {/if}
                    <div class="mt-4 flex items-center justify-between text-xs font-medium" style="color: {brandColor};">
                      <span class="flex items-center gap-1.5">
                        {#if form.config?.require_auth}
                          <LockKeyhole class="h-3.5 w-3.5" /> Sign-in required
                        {:else}
                          Open form
                        {/if}
                      </span>
                      <ArrowRight class="h-4 w-4 transition-transform group-hover:translate-x-0.5" />
                    </div>
                  </div>
                </div>
              </button>
            {/each}
          </div>
        {/if}
      </div>
    </main>

    <!-- Footer (hidden in embed mode) -->
    {#if !embed}
      <footer class="py-4 text-center">
        <p class="text-xs" style="color: {isDarkMode ? '#475569' : '#9ca3af'};">
          Powered by Windshift
        </p>
      </footer>
    {/if}
  {/if}
</div>

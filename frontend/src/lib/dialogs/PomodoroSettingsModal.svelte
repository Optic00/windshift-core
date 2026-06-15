<script>
  import { Clock, FileText, Play, X } from '@lucide/svelte';
  import Button from '../components/Button.svelte';
  import FormField from '../components/FormField.svelte';
  import Input from '../components/Input.svelte';
  import ModalBackdrop from '../components/ModalBackdrop.svelte';
  import Select from '../components/Select.svelte';
  import Toggle from '../components/Toggle.svelte';
  import { api } from '../api.js';
  import { t } from '../stores/i18n.svelte.js';
  import { getShortcut, matchesShortcut } from '../utils/keyboardShortcuts.js';

  let {
    show = $bindable(false),
    onclose = null,
  } = $props();

  const submitShortcut = getShortcut('modal', 'submit');
  const defaultForm = {
    work_duration_minutes: 25,
    short_break_minutes: 5,
    long_break_minutes: 15,
    cycles_before_long_break: 4,
    auto_start_break: true,
    auto_start_work: false,
    auto_log_enabled: false,
    log_project_id: null,
    log_description: '',
  };

  let invoke = null;
  let loaded = $state(false);
  let loading = $state(false);
  let saving = $state(false);
  let error = $state('');
  let status = $state('');
  let projects = $state([]);
  let projectsLoaded = $state(false);
  let form = $state({ ...defaultForm });

  const projectOptions = $derived([
    { value: '', label: 'Select a project...' },
    ...projects
      .filter((project) => project.status === 'Active')
      .map((project) => ({
        value: project.id,
        label: project.customer_name ? `${project.name} (${project.customer_name})` : project.name,
      })),
  ]);

  $effect(() => {
    if (show && !loaded && !loading) {
      load();
    }
  });

  $effect(() => {
    if (show && form.auto_log_enabled && !projectsLoaded) {
      loadProjects();
    }
  });

  async function getInvoke() {
    if (invoke) return invoke;
    const core = await import('@tauri-apps/api/core');
    invoke = core.invoke;
    return invoke;
  }

  function toForm(settings) {
    return {
      work_duration_minutes: Math.round((settings.work_duration_secs ?? 1500) / 60),
      short_break_minutes: Math.round((settings.short_break_secs ?? 300) / 60),
      long_break_minutes: Math.round((settings.long_break_secs ?? 900) / 60),
      cycles_before_long_break: settings.cycles_before_long_break ?? 4,
      auto_start_break: !!settings.auto_start_break,
      auto_start_work: !!settings.auto_start_work,
      auto_log_enabled: !!settings.auto_log_enabled,
      log_project_id: settings.log_project_id ?? null,
      log_description: settings.log_description ?? '',
    };
  }

  function positiveInteger(value, fallback) {
    const parsed = parseInt(value, 10);
    return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
  }

  function toSettings(current) {
    return {
      work_duration_secs: positiveInteger(current.work_duration_minutes, 25) * 60,
      short_break_secs: positiveInteger(current.short_break_minutes, 5) * 60,
      long_break_secs: positiveInteger(current.long_break_minutes, 15) * 60,
      cycles_before_long_break: positiveInteger(current.cycles_before_long_break, 4),
      auto_start_break: current.auto_start_break,
      auto_start_work: current.auto_start_work,
      auto_log_enabled: current.auto_log_enabled,
      log_project_id: current.auto_log_enabled && current.log_project_id ? Number(current.log_project_id) : null,
      log_item_id: null,
      log_description: current.log_description?.trim() || null,
    };
  }

  async function load() {
    loading = true;
    error = '';
    status = '';
    try {
      const call = await getInvoke();
      form = toForm(await call('get_pomodoro_settings'));
      loaded = true;
    } catch (err) {
      console.warn('[pomodoro-settings] failed to load settings:', err);
      error = `Failed to load settings: ${err?.message || err}`;
    } finally {
      loading = false;
    }
  }

  async function loadProjects() {
    projectsLoaded = true;
    try {
      projects = (await api.time.projects.getAll()) || [];
    } catch (err) {
      console.warn('[pomodoro-settings] failed to load projects:', err);
      projects = [];
    }
  }

  async function save() {
    if (saving || loading) return;
    saving = true;
    error = '';
    status = '';
    try {
      const call = await getInvoke();
      await call('save_pomodoro_settings', { settings: toSettings(form) });
      status = 'Settings saved';
    } catch (err) {
      console.warn('[pomodoro-settings] failed to save settings:', err);
      error = `Failed to save settings: ${err?.message || err}`;
    } finally {
      saving = false;
    }
  }

  function close() {
    onclose?.();
    show = false;
  }

  function handleKeydown(event) {
    if (!show) return;
    if (matchesShortcut(event, submitShortcut)) {
      event.preventDefault();
      save();
    }
  }
</script>

<svelte:window onkeydown={handleKeydown} />

<!-- shortcut-guard-exempt: Cmd+Enter submit is handled via svelte:window onkeydown (matchesShortcut) above, outside the ModalBackdrop block the guard scans. -->
<ModalBackdrop bind:show onclose={close} ariaLabelledBy="pomodoro-settings-title" zIndex={70} scrollable align="top" paddingTop="pt-8">
  <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
  <div
    role="presentation"
    class="mb-8 w-full max-w-3xl rounded shadow-xl"
    style="background-color: var(--ds-surface-raised); color: var(--ds-text);"
    onclick={(e) => e.stopPropagation()}
  >
    <div class="flex items-center justify-between border-b px-6 py-4" style="border-color: var(--ds-border);">
      <div>
        <h2 id="pomodoro-settings-title" class="text-lg font-semibold">Pomodoro Settings</h2>
        <p class="mt-1 text-sm" style="color: var(--ds-text-subtle);">Configure desktop timer behavior and automatic time logging.</p>
      </div>
      <Button variant="ghost" icon={X} onclick={close} title={t('common.close')} />
    </div>

    <div class="px-6 py-5">
      {#if loading}
        <p class="text-sm" style="color: var(--ds-text-subtle);">Loading settings...</p>
      {:else}
        <div class="grid gap-6 md:grid-cols-3">
          <section class="md:col-span-2">
            <div class="mb-3 flex items-center gap-2">
              <Clock class="h-5 w-5" style="color: var(--ds-icon);" />
              <h3 class="text-sm font-semibold uppercase tracking-wide" style="color: var(--ds-text-subtle);">Timer</h3>
            </div>

            <div class="grid gap-x-4 md:grid-cols-2">
              <FormField label="Work duration" helper="Minutes per focus session">
                <Input type="number" min="1" step="1" bind:value={form.work_duration_minutes} />
              </FormField>
              <FormField label="Short break" helper="Minutes between work sessions">
                <Input type="number" min="1" step="1" bind:value={form.short_break_minutes} />
              </FormField>
              <FormField label="Long break" helper="Minutes after a completed cycle">
                <Input type="number" min="1" step="1" bind:value={form.long_break_minutes} />
              </FormField>
              <FormField label="Sessions before long break" helper="Completed work sessions">
                <Input type="number" min="1" step="1" bind:value={form.cycles_before_long_break} />
              </FormField>
            </div>
          </section>

          <section>
            <div class="mb-3 flex items-center gap-2">
              <Play class="h-5 w-5" style="color: var(--ds-icon);" />
              <h3 class="text-sm font-semibold uppercase tracking-wide" style="color: var(--ds-text-subtle);">Behavior</h3>
            </div>
            <div class="space-y-4 rounded border p-4" style="border-color: var(--ds-border); background-color: var(--ds-surface);">
              <Toggle bind:checked={form.auto_start_break} label="Auto-start breaks" />
              <Toggle bind:checked={form.auto_start_work} label="Auto-start work" />
            </div>
          </section>
        </div>

        <section class="mt-6">
          <div class="mb-3 flex items-center gap-2">
            <FileText class="h-5 w-5" style="color: var(--ds-icon);" />
            <h3 class="text-sm font-semibold uppercase tracking-wide" style="color: var(--ds-text-subtle);">Logging</h3>
          </div>

          <div class="rounded border p-4" style="border-color: var(--ds-border); background-color: var(--ds-surface);">
            <Toggle bind:checked={form.auto_log_enabled} label="Automatically log completed work sessions" />

            {#if form.auto_log_enabled}
              <div class="mt-4 grid gap-x-4 md:grid-cols-2">
                <FormField label="Project" helper="Active time projects from this workspace server">
                  <Select
                    bind:value={form.log_project_id}
                    options={projectOptions}
                    placeholder={projectsLoaded ? 'Select a project...' : 'Loading projects...'}
                  />
                </FormField>
                <FormField label="Description" helper="Used for new worklog entries">
                  <Input bind:value={form.log_description} placeholder="Pomodoro session" />
                </FormField>
              </div>
            {/if}
          </div>
        </section>

        {#if error}
          <div class="mt-4 rounded border px-4 py-3 text-sm" style="border-color: var(--ds-border-danger); color: var(--ds-text-danger); background-color: var(--ds-background-danger);">
            {error}
          </div>
        {:else if status}
          <div class="mt-4 rounded border px-4 py-3 text-sm" style="border-color: var(--ds-border-success); color: var(--ds-text-success); background-color: var(--ds-background-success);">
            {status}
          </div>
        {/if}
      {/if}
    </div>

    <div class="flex justify-end gap-3 border-t px-6 py-4" style="border-color: var(--ds-border);">
      <Button variant="default" size="small" onclick={close}>Cancel</Button>
      <Button variant="primary" size="small" onclick={save} loading={saving} disabled={loading}>Save Settings</Button>
    </div>
  </div>
</ModalBackdrop>

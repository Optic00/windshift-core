<script>
  import { onMount } from 'svelte';

  let {
    baseUrl,
    slug,
    formId = null,
    prefill = {},
    theme = 'auto',
    onSuccess = () => {},
    onError = () => {},
  } = $props();

  let channel = $state(null);
  let forms = $state([]);
  let fields = $state([]);
  let customFieldDefinitions = $state([]);
  let selectedFormId = $state(null);
  let loadedDetailFormId = $state(null);
  let loading = $state(true);
  let fieldsLoading = $state(false);
  let submitting = $state(false);
  let error = $state('');
  let success = $state('');
  let values = $state({ title: '', description: '', custom_fields: {} });

  let brandColor = $derived(channel?.brand_color || '#14b8a6');
  let effectiveTheme = $derived(
    theme === 'dark' ||
      channel?.theme === 'dark' ||
      ((theme === 'auto' || !theme) &&
        channel?.theme === 'auto' &&
        globalThis.matchMedia?.('(prefers-color-scheme: dark)').matches)
      ? 'dark'
      : 'light'
  );
  let selectedForm = $derived(forms.find((form) => form.id === selectedFormId));

  onMount(() => {
    selectedFormId = formId ? Number(formId) : null;
    load();
  });

  $effect(() => {
    if (selectedFormId && loadedDetailFormId !== selectedFormId) loadFields();
  });

  async function request(path, options = {}) {
    const response = await fetch(`${baseUrl}/api${path}`, {
      ...options,
      credentials: 'omit',
      headers: {
        Accept: 'application/json',
        ...(options.body ? { 'Content-Type': 'application/json' } : {}),
        ...options.headers,
      },
    });

    if (!response.ok) {
      let message = response.statusText || 'Request failed';
      try {
        const body = await response.json();
        message = body.error || body.message || message;
      } catch {
        // Keep fallback.
      }
      throw Object.assign(new Error(message), { status: response.status });
    }

    if (response.status === 204) return null;
    return response.json();
  }

  async function load() {
    try {
      loading = true;
      error = '';
      const bootstrap = await request(`/forms/${encodeURIComponent(slug)}/bootstrap`);
      channel = bootstrap.channel;
      forms = bootstrap.forms || [];
      const nextFormId = selectedFormId || (forms.length === 1 ? forms[0].id : null);
      if (bootstrap.form_detail?.form_id === nextFormId) applyDetail(bootstrap.form_detail);
      selectedFormId = nextFormId;
    } catch (err) {
      error = err.message || 'Unable to load form';
      onError(err);
    } finally {
      loading = false;
    }
  }

  async function loadFields() {
    try {
      fieldsLoading = true;
      error = '';
      const detail = await request(
        `/forms/${encodeURIComponent(slug)}/forms/${encodeURIComponent(selectedFormId)}/detail`
      );
      applyDetail(detail);
    } catch (err) {
      error = err.message || 'Unable to load form fields';
      onError(err);
    } finally {
      fieldsLoading = false;
    }
  }

  function applyDetail(detail) {
    fields = detail.fields || [];
    customFieldDefinitions = detail.custom_field_definitions || [];
    values = initialValues(fields);
    loadedDetailFormId = detail.form_id;
  }

  function initialValues(nextFields) {
    const next = {
      title: prefill.title || '',
      description: prefill.description || '',
      custom_fields: { ...(prefill.customFields || {}) },
    };

    for (const field of nextFields) {
      if (field.field_type === 'default') continue;
      const key = field.field_identifier;
      if (next.custom_fields[key] !== undefined) continue;
      if (key === 'email' && prefill.email) next.custom_fields[key] = prefill.email;
      else if (key === 'name' && prefill.name) next.custom_fields[key] = prefill.name;
      else if (fieldKind(field) === 'checkbox') next.custom_fields[key] = false;
      else if (fieldKind(field) === 'multiselect') next.custom_fields[key] = [];
      else next.custom_fields[key] = '';
    }
    return next;
  }

  function labelFor(field) {
    return field.display_name || field.field_label || field.field_name || field.field_identifier;
  }

  function customFieldDefinition(field) {
    return customFieldDefinitions.find(
      (definition) => String(definition.id) === String(field.field_identifier)
    );
  }

  function fieldKind(field) {
    if (field.field_type === 'custom') return customFieldDefinition(field)?.field_type || 'text';
    if (field.field_type === 'virtual') return field.virtual_field_type || 'text';
    return field.field_identifier === 'description' ? 'textarea' : 'text';
  }

  function optionsFor(field) {
    const raw =
      field.field_type === 'custom'
        ? customFieldDefinition(field)?.options
        : field.virtual_field_options;
    if (!raw) return [];
    if (Array.isArray(raw)) return normalizeOptions(raw);
    try {
      const parsed = JSON.parse(raw);
      return normalizeOptions(Array.isArray(parsed) ? parsed : parsed?.items || []);
    } catch {
      return [];
    }
  }

  function normalizeOptions(options) {
    return options.map((option) => ({
      value: option?.id ?? option?.value ?? option,
      label: option?.label ?? String(option),
    }));
  }

  function selectValue(field, rawValue) {
    if (field.field_type !== 'custom' || rawValue === '') return rawValue;
    const numeric = Number(rawValue);
    return Number.isFinite(numeric) ? numeric : rawValue;
  }

  function valueFor(field) {
    if (field.field_type === 'default') return values[field.field_identifier] || '';
    return values.custom_fields[field.field_identifier] ?? '';
  }

  function setValue(field, value) {
    if (field.field_type === 'default') {
      values = { ...values, [field.field_identifier]: value };
    } else {
      values = {
        ...values,
        custom_fields: { ...values.custom_fields, [field.field_identifier]: value },
      };
    }
  }

  function validate() {
    for (const field of fields) {
      if (!field.is_required) continue;
      const value = valueFor(field);
      if (
        value === undefined ||
        value === null ||
        value === '' ||
        (Array.isArray(value) && value.length === 0)
      ) {
        error = `${labelFor(field)} is required`;
        return false;
      }
    }
    return true;
  }

  async function submit() {
    try {
      error = '';
      success = '';
      if (!validate()) return;
      submitting = true;
      const result = await request(`/forms/${encodeURIComponent(slug)}/submit`, {
        method: 'POST',
        body: JSON.stringify({
          request_type_id: selectedFormId,
          title: values.title,
          description: values.description,
          custom_fields: values.custom_fields,
        }),
      });
      success = result?.success_message || 'Thank you for your submission!';
      onSuccess(result);
    } catch (err) {
      error = err.message || 'Unable to submit form';
      onError(err);
    } finally {
      submitting = false;
    }
  }
</script>

<div class="wsf-root" data-theme={effectiveTheme} style={`--wsf-brand: ${brandColor}`}>
  <div class="wsf-card">
    {#if loading}
      <div class="wsf-loading">Loading form…</div>
    {:else if error && !selectedFormId}
      <div class="wsf-notice wsf-notice-error">{error}</div>
    {:else if !selectedFormId}
      {#if channel?.name}<h2 class="wsf-title">{channel.name}</h2>{/if}
      <div class="wsf-description">Choose a form to continue.</div>
      {#each forms as form}
        <div class="wsf-field">
          <button class="wsf-button wsf-button-secondary" type="button" onclick={() => (selectedFormId = form.id)}>
            {form.name}
          </button>
        </div>
      {/each}
    {:else if success}
      <div id="wsf-success" class="wsf-notice wsf-notice-success">{success}</div>
    {:else}
      {#if selectedForm}
        <h2 class="wsf-title">{selectedForm.name}</h2>
        {#if selectedForm.description}<p class="wsf-description">{selectedForm.description}</p>{/if}
      {/if}

      {#if fieldsLoading}
        <div class="wsf-loading">Loading fields…</div>
      {:else}
        {#if error}<div class="wsf-notice wsf-notice-error">{error}</div>{/if}
        <form onsubmit={(event) => { event.preventDefault(); submit(); }}>
          {#each fields as field}
            {@const kind = fieldKind(field)}
            <div class="wsf-field">
              {#if kind === 'checkbox'}
                <label class="wsf-checkbox">
                  <input
                    id={`wsf-${field.field_identifier}`}
                    type="checkbox"
                    checked={Boolean(valueFor(field))}
                    onchange={(event) => setValue(field, event.currentTarget.checked)}
                  />
                  <span>{labelFor(field)} {#if field.is_required}<span class="wsf-required">*</span>{/if}</span>
                </label>
              {:else}
                <label class="wsf-label" for={`wsf-${field.field_identifier}`}>
                  {labelFor(field)} {#if field.is_required}<span class="wsf-required">*</span>{/if}
                </label>
                {#if kind === 'textarea'}
                  <textarea
                    class="wsf-textarea"
                    id={`wsf-${field.field_identifier}`}
                    value={valueFor(field)}
                    oninput={(event) => setValue(field, event.currentTarget.value)}
                  ></textarea>
                {:else if kind === 'select'}
                  <select
                    class="wsf-select"
                    id={`wsf-${field.field_identifier}`}
                    value={valueFor(field)}
                    onchange={(event) => setValue(field, selectValue(field, event.currentTarget.value))}
                  >
                    <option value="">Select…</option>
                    {#each optionsFor(field) as option}
                      <option value={option.value}>{option.label}</option>
                    {/each}
                  </select>
                {:else if kind === 'multiselect'}
                  <select
                    class="wsf-select"
                    id={`wsf-${field.field_identifier}`}
                    multiple
                    onchange={(event) =>
                      setValue(
                        field,
                        [...event.currentTarget.selectedOptions].map((option) =>
                          selectValue(field, option.value)
                        )
                      )}
                  >
                    {#each optionsFor(field) as option}
                      <option value={option.value}>{option.label}</option>
                    {/each}
                  </select>
                {:else}
                  <input
                    class="wsf-input"
                    id={`wsf-${field.field_identifier}`}
                    type={kind === 'email' ? 'email' : kind === 'date' ? 'date' : kind === 'number' ? 'number' : 'text'}
                    step={kind === 'number' ? 'any' : undefined}
                    value={valueFor(field)}
                    oninput={(event) =>
                      setValue(
                        field,
                        kind === 'number' && event.currentTarget.value !== ''
                          ? Number(event.currentTarget.value)
                          : event.currentTarget.value
                      )}
                  />
                {/if}
              {/if}
              {#if field.help_text}<div class="wsf-help">{field.help_text}</div>{/if}
            </div>
          {/each}
          <div class="wsf-actions">
            <button id="wsf-submit" class="wsf-button" type="submit" disabled={submitting}>
              {submitting ? 'Submitting…' : selectedForm?.config?.submit_button_text || 'Submit'}
            </button>
          </div>
        </form>
      {/if}
    {/if}
  </div>
</div>

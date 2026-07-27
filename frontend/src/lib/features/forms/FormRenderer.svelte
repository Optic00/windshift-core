<script>
  import { untrack } from 'svelte';
  import { CheckCircle2, ExternalLink, RotateCcw } from '@lucide/svelte';
  import { api } from '../../api.js';
  import { t } from '../../stores/i18n.svelte.js';
  import Spinner from '../../components/Spinner.svelte';
  import Label from '../../components/Label.svelte';
  import AlertBox from '../../components/AlertBox.svelte';
  import Button from '../../components/Button.svelte';
  import Progress from '../../components/Progress.svelte';
  import { toExternal } from '../../runtime/contextPath.js';
  import { loadPublicFormDetail } from './publicFormData.js';
  import FormFields from './FormFields.svelte';
  import {
    buildFormSteps,
    clampFormStep,
    initializeFormValues,
    validateFormStep,
  } from './formModel.js';
  import {
    clearPublicFormDraft,
    loadPublicFormDraft,
    savePublicFormDraft,
  } from './publicFormDrafts.js';

  let {
    formSlug = '',
    formId = null,
    formConfig = null,
    attachmentConfig = null,
    initialDetail = null,
    brandColor = null,
    isDarkMode = false,
    initialValues = null,
    submitForm = null,
    authenticationRequired = false,
    embed = false,
    preview = false,
    returnPath = '',
    onSubmitted = () => {},
  } = $props();

  let submitButtonText = $derived(formConfig?.submit_button_text || 'Submit');
  let fields = $state([]);
  let customFieldDefinitions = $state([]);
  let activeDetail = $state(null);
  let loading = $state(true);
  let submitting = $state(false);
  let error = $state(null);
  let submissionRequiresAuth = $state(false);
  let success = $state(false);
  let successMessage = $state('');
  let redirectUrl = $state('');
  let loadSequence = 0;

  let steps = $state([1]);
  let currentStep = $state(1);
  let totalSteps = $derived(steps.length);
  let isLastStep = $derived(currentStep === Math.max(...steps));
  let isFirstStep = $derived(currentStep === Math.min(...steps));

  let formData = $state({ title: '', description: '' });
  let customFieldValues = $state({});
  let attachments = $state([]);

  let draftReady = $state(false);
  let resumedDraft = $state(null);
  let draftStatus = $state('');
  let draftStatusTimer = null;
  let needsAuthentication = $derived(
    !preview && (authenticationRequired || submissionRequiresAuth)
  );
  let hostedFormPath = $derived(`/forms/${encodeURIComponent(formSlug)}/${formId}`);
  let signInReturnPath = $derived(
    returnPath || (typeof window !== 'undefined' ? `${window.location.pathname}${window.location.search}` : hostedFormPath)
  );
  let signInUrl = $derived(
    toExternal(`/login?return_to=${encodeURIComponent(signInReturnPath)}`)
  );

  $effect(() => {
    const activeSlug = formSlug;
    const activeFormId = formId;
    const seededDetail = initialDetail;
    if (!activeSlug || !activeFormId) return;

    const sequence = ++loadSequence;
    untrack(() => {
      if (seededDetail?.form_id === activeFormId) {
        applyDetail(seededDetail);
      } else {
        void loadFields(activeSlug, activeFormId, sequence);
      }
    });
  });

  $effect(() => {
    const snapshot = {
      title: formData.title,
      description: formData.description,
      custom_fields: JSON.parse(JSON.stringify(customFieldValues)),
      current_step: currentStep,
    };
    if (!draftReady || preview || success || typeof localStorage === 'undefined') return;

    draftStatus = 'saving';
    const timer = setTimeout(() => {
      const saved = savePublicFormDraft(localStorage, formSlug, formId, snapshot);
      draftStatus = saved ? 'saved' : '';
      if (saved) {
        if (draftStatusTimer) clearTimeout(draftStatusTimer);
        draftStatusTimer = setTimeout(() => {
          draftStatus = '';
          draftStatusTimer = null;
        }, 1800);
      }
    }, 300);

    return () => clearTimeout(timer);
  });

  $effect(() => {
    return () => {
      if (draftStatusTimer) clearTimeout(draftStatusTimer);
    };
  });

  function applyDetail(detail) {
    draftReady = false;
    activeDetail = detail;
    error = null;
    submissionRequiresAuth = false;
    success = false;
    fields = detail.fields || [];
    customFieldDefinitions = detail.custom_field_definitions || [];
    steps = buildFormSteps(fields);

    let values = initializeFormValues(fields, initialValues);
    let nextStep = steps[0];
    resumedDraft = null;

    if (!preview && !initialValues && typeof localStorage !== 'undefined') {
      const draft = loadPublicFormDraft(localStorage, formSlug, formId);
      if (draft) {
        values = initializeFormValues(fields, draft);
        nextStep = clampFormStep(steps, draft.current_step);
        resumedDraft = draft;
      }
    }

    formData = values.formData;
    customFieldValues = values.customFieldValues;
    currentStep = nextStep;
    attachments = [];
    loading = false;
    queueMicrotask(() => {
      draftReady = true;
    });
  }

  async function loadFields(activeSlug, activeFormId, sequence) {
    try {
      loading = true;
      error = null;
      submissionRequiresAuth = false;
      success = false;

      const detail = await loadPublicFormDetail(activeSlug, activeFormId);
      if (sequence !== loadSequence) return;
      applyDetail(detail);
    } catch (err) {
      if (sequence !== loadSequence) return;
      console.error('Failed to load form fields:', err);
      error = err.message || 'Failed to load form fields';
    } finally {
      if (sequence === loadSequence) loading = false;
    }
  }

  function validateCurrentStep() {
    const message = validateFormStep({
      fields,
      step: currentStep,
      formData,
      customFieldValues,
    });
    error = message || null;
    return !message;
  }

  function goToNextStep() {
    error = null;
    if (!validateCurrentStep()) return;
    const currentIndex = steps.indexOf(currentStep);
    if (currentIndex < steps.length - 1) currentStep = steps[currentIndex + 1];
  }

  function goToPrevStep() {
    error = null;
    const currentIndex = steps.indexOf(currentStep);
    if (currentIndex > 0) currentStep = steps[currentIndex - 1];
  }

  function handleAttachmentChange(event) {
    attachments = Array.from(event.currentTarget.files || []);
    error = null;
  }

  function startFresh() {
    if (typeof localStorage !== 'undefined') {
      clearPublicFormDraft(localStorage, formSlug, formId);
    }
    resumedDraft = null;
    if (activeDetail) applyDetail(activeDetail);
  }

  function submitAnother() {
    success = false;
    successMessage = '';
    redirectUrl = '';
    if (activeDetail) applyDetail(activeDetail);
  }

  async function handleSubmit() {
    if (preview || needsAuthentication) return;

    try {
      for (const step of steps) {
        currentStep = step;
        if (!validateCurrentStep()) return;
      }
      currentStep = Math.max(...steps);

      submitting = true;
      error = null;
      submissionRequiresAuth = false;

      if (attachmentConfig?.enabled) {
        const maxFiles = attachmentConfig.max_files || 5;
        if (attachments.length > maxFiles) {
          error = `You can attach at most ${maxFiles} files.`;
          return;
        }
        const oversized = attachments.find((file) => file.size > attachmentConfig.max_file_size);
        if (oversized) {
          error = `${oversized.name} exceeds the attachment size limit.`;
          return;
        }
      }

      const submissionData = {
        request_type_id: formId,
        title: formData.title,
        description: formData.description,
        custom_fields: customFieldValues,
      };

      const result = submitForm
        ? await submitForm(submissionData, attachments)
        : await api.forms.submit(formSlug, submissionData, attachments);

      draftReady = false;
      if (typeof localStorage !== 'undefined') {
        clearPublicFormDraft(localStorage, formSlug, formId);
      }
      success = true;
      successMessage = result.success_message || 'Thank you for your submission!';
      redirectUrl = result.redirect_url || '';
      onSubmitted(result);

      if (redirectUrl) {
        setTimeout(() => {
          window.location.href = toExternal(redirectUrl);
        }, 2000);
      }
    } catch (err) {
      console.error('Failed to submit form:', err);
      submissionRequiresAuth = err.status === 403;
      error = err.message || 'Failed to submit form';
    } finally {
      submitting = false;
    }
  }
</script>

<div
  class:ds-brand-scope={Boolean(brandColor)}
  style={brandColor ? `--ds-brand-color: ${brandColor}` : undefined}
>
  {#if loading}
    <div class="flex items-center justify-center py-12">
      <Spinner />
    </div>
  {:else if success}
    <div class="py-8 text-center" data-testid="public-form-success">
      <div
        class="mx-auto mb-4 flex h-14 w-14 items-center justify-center rounded-full"
        style="background: color-mix(in srgb, var(--ds-text-success) 12%, transparent); color: var(--ds-text-success);"
      >
        <CheckCircle2 class="h-7 w-7" />
      </div>
      <h3 class="text-xl font-semibold" style="color: var(--ds-text);">Response received</h3>
      <p class="mx-auto mt-2 max-w-md text-sm" style="color: var(--ds-text-subtle);">{successMessage}</p>
      <div class="mt-6 flex flex-wrap justify-center gap-2">
        <Button
          type="button"
          variant="default"
          icon={RotateCcw}
          dataTestid="public-form-submit-another"
          onclick={submitAnother}
        >
          Submit another response
        </Button>
        {#if redirectUrl}
          <Button
            type="button"
            variant="primary"
            icon={ExternalLink}
            dataTestid="public-form-success-continue"
            onclick={() => {
              window.location.href = toExternal(redirectUrl);
            }}
          >
            Continue
          </Button>
        {/if}
      </div>
      {#if redirectUrl}
        <p class="mt-3 text-xs" style="color: var(--ds-text-subtle);">Redirecting shortly…</p>
      {/if}
    </div>
  {:else if needsAuthentication}
    <div class="py-8 text-center" data-testid="form-auth-required">
      <div class="mx-auto max-w-md">
        <AlertBox
          variant="warning"
          message={embed
            ? 'This form requires a Windshift sign-in and cannot be completed inside an embed.'
            : 'Sign in to continue. Your saved progress will be restored when you return.'}
        />
        <a
          data-testid="public-form-auth-action"
          class="mt-4 inline-flex items-center gap-2 rounded-md px-4 py-2 text-sm font-semibold text-white"
          style="background: var(--ds-interactive);"
          href={embed ? toExternal(hostedFormPath) : signInUrl}
          target={embed ? '_blank' : undefined}
        >
          {embed ? 'Open hosted form' : 'Sign in and return'}
          <ExternalLink class="h-4 w-4" />
        </a>
      </div>
    </div>
  {:else}
    {#if preview}
      <div class="mb-4">
        <AlertBox variant="info" message="Preview mode — submissions are disabled." />
      </div>
    {:else if resumedDraft}
      <div
        data-testid="public-form-draft-resume"
        class="mb-4 flex items-center justify-between gap-3 rounded-lg border p-3"
        style="border-color: var(--ds-border); background: var(--ds-background-neutral);"
      >
        <p class="text-sm" style="color: var(--ds-text-subtle);">Your saved progress was restored.</p>
        <button
          type="button"
          data-testid="public-form-draft-start-fresh"
          class="text-sm font-medium hover:underline"
          style="color: var(--ds-text-link);"
          onclick={startFresh}
        >
          Start fresh
        </button>
      </div>
    {/if}

    <form onsubmit={(event) => { event.preventDefault(); isLastStep ? handleSubmit() : goToNextStep(); }}>
      {#if error}
        <AlertBox variant="error" message={error} class="mb-4" />
      {/if}

      {#if totalSteps > 1}
        <div class="mb-6">
          <div class="mb-2 flex items-center justify-between text-xs font-medium" style="color: var(--ds-text-subtle);">
            <span>Step {steps.indexOf(currentStep) + 1} of {totalSteps}</span>
            <span>{Math.round(((steps.indexOf(currentStep) + 1) / totalSteps) * 100)}%</span>
          </div>
          <Progress value={steps.indexOf(currentStep) + 1} max={totalSteps} size="sm" />
        </div>
      {/if}

      <FormFields
        {fields}
        {customFieldDefinitions}
        {currentStep}
        bind:formData
        bind:customFieldValues
        {isDarkMode}
        idPrefix="form"
      />

      {#if isLastStep && attachmentConfig?.enabled}
        <div class="mt-4">
          <Label for="form-attachments" class="mb-1.5" color="default">Attachments</Label>
          <input
            id="form-attachments"
            data-testid="public-form-attachments"
            type="file"
            multiple
            accept={attachmentConfig.allowed_mime_types?.join(',') || undefined}
            onchange={handleAttachmentChange}
          />
          <p class="mt-1 text-xs" style="color: var(--ds-text-subtle);">
            Up to {attachmentConfig.max_files || 5} files, {Math.floor(attachmentConfig.max_file_size / 1048576)} MB each.
          </p>
          {#if attachments.length > 0}
            <ul data-testid="public-form-attachment-list" class="mt-2 space-y-1 text-xs" style="color: var(--ds-text-subtle);">
              {#each attachments as attachment}
                <li>{attachment.name}</li>
              {/each}
            </ul>
          {/if}
        </div>
      {/if}

      <div class="mt-6 flex items-center justify-between border-t pt-4" style="border-color: var(--ds-border);">
        <div class="flex items-center gap-3">
          {#if !isFirstStep}
            <Button
              type="button"
              variant="default"
              dataTestid="public-form-back-step"
              onclick={goToPrevStep}
            >
              Back
            </Button>
          {/if}
          {#if !preview && draftStatus}
            <span class="text-xs" data-testid="public-form-draft-status" style="color: var(--ds-text-subtle);">
              {draftStatus === 'saving' ? 'Saving draft…' : 'Draft saved'}
            </span>
          {/if}
        </div>

        <Button
          type="submit"
          variant="primary"
          dataTestid="public-form-submit"
          disabled={submitting || (preview && isLastStep)}
          loading={submitting}
        >
          {isLastStep ? (preview ? 'Preview only' : submitButtonText) : 'Next'}
        </Button>
      </div>
    </form>
  {/if}
</div>

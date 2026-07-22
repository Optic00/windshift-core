<script>
  import { untrack } from 'svelte';
  import { api } from '../../api.js';
  import { t } from '../../stores/i18n.svelte.js';
  import CustomFieldRenderer from '../items/CustomFieldRenderer.svelte';
  import Spinner from '../../components/Spinner.svelte';
  import Label from '../../components/Label.svelte';
  import AlertBox from '../../components/AlertBox.svelte';
  import Button from '../../components/Button.svelte';
  import Checkbox from '../../components/Checkbox.svelte';
  import Input from '../../components/Input.svelte';
  import NativeSelect from '../../components/NativeSelect.svelte';
  import Progress from '../../components/Progress.svelte';
  import Textarea from '../../components/Textarea.svelte';
  import { toExternal } from '../../runtime/contextPath.js';
  import { loadPublicFormDetail } from './publicFormData.js';

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
    onSubmitted = () => {},
  } = $props();

  let submitButtonText = $derived(formConfig?.submit_button_text || 'Submit');

  let fields = $state([]);
  let customFieldDefinitions = $state([]);
  let loading = $state(true);
  let submitting = $state(false);
  let error = $state(null);
  let success = $state(false);
  let successMessage = $state('');
  let redirectUrl = $state('');
  let loadSequence = 0;

  // Multi-step
  let steps = $state([1]);
  let currentStep = $state(1);
  let currentStepFields = $derived(fields.filter(f => (f.step_number || 1) === currentStep));
  let totalSteps = $derived(steps.length);
  let isLastStep = $derived(currentStep === Math.max(...steps));
  let isFirstStep = $derived(currentStep === Math.min(...steps));

  // Form data
  let formData = $state({ title: '', description: '' });
  let customFieldValues = $state({});
  let attachments = $state([]);

  // The sole-form bootstrap already contains its complete render data. For a
  // multi-form channel, selection uses one complete-detail request.
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

  function applyDetail(detail) {
    error = null;
    success = false;
    fields = detail.fields || [];
    customFieldDefinitions = detail.custom_field_definitions || [];

    const stepNumbers = [...new Set(fields.map(f => f.step_number || 1))].sort((a, b) => a - b);
    steps = stepNumbers.length > 0 ? stepNumbers : [1];
    currentStep = Math.min(...steps);

    formData = {
      title: initialValues?.title || '',
      description: initialValues?.description || '',
    };
    customFieldValues = {};
    fields.forEach(field => {
      if (field.field_type === 'custom' || field.field_type === 'virtual') {
        if (initialValues?.custom_fields?.[field.field_identifier] !== undefined) {
          customFieldValues[field.field_identifier] = initialValues.custom_fields[field.field_identifier];
        } else if (field.field_type === 'virtual' && field.virtual_field_type === 'checkbox') {
          customFieldValues[field.field_identifier] = false;
        } else {
          customFieldValues[field.field_identifier] = '';
        }
      }
    });
    loading = false;
  }

  async function loadFields(activeSlug, activeFormId, sequence) {
    try {
      loading = true;
      error = null;
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

  function getFieldLabel(field) {
    return field.display_name || field.field_label || field.field_name || field.field_identifier;
  }

  function getCustomFieldDefinition(fieldId) {
    return customFieldDefinitions.find(f => f.id.toString() === fieldId);
  }

  function hasFieldInCurrentStep(fieldIdentifier) {
    return currentStepFields.some(f => f.field_identifier === fieldIdentifier);
  }

  function validateCurrentStep() {
    for (const field of currentStepFields) {
      if (!field.is_required) continue;

      if (field.field_type === 'default') {
        if (field.field_identifier === 'title' && !formData.title.trim()) {
          error = `${getFieldLabel(field)} is required`;
          return false;
        }
        if (field.field_identifier === 'description' && !formData.description.trim()) {
          error = `${getFieldLabel(field)} is required`;
          return false;
        }
      } else if (field.field_type === 'custom') {
        const value = customFieldValues[field.field_identifier];
        if (value === undefined || value === null || value === '') {
          error = `${getFieldLabel(field)} is required`;
          return false;
        }
      } else if (field.field_type === 'virtual') {
        const value = customFieldValues[field.field_identifier];
        if (field.virtual_field_type !== 'checkbox' && (value === undefined || value === null || value === '')) {
          error = `${getFieldLabel(field)} is required`;
          return false;
        }
      }
    }
    return true;
  }

  function goToNextStep() {
    error = null;
    if (!validateCurrentStep()) return;
    const currentIndex = steps.indexOf(currentStep);
    if (currentIndex < steps.length - 1) {
      currentStep = steps[currentIndex + 1];
    }
  }

  function goToPrevStep() {
    error = null;
    const currentIndex = steps.indexOf(currentStep);
    if (currentIndex > 0) {
      currentStep = steps[currentIndex - 1];
    }
  }

  function parseSelectOptions(optionsJson) {
    try {
      return JSON.parse(optionsJson) || [];
    } catch {
      return [];
    }
  }

  function handleAttachmentChange(event) {
    attachments = Array.from(event.currentTarget.files || []);
    error = null;
  }

  async function handleSubmit() {
    try {
      for (const step of steps) {
        currentStep = step;
        if (!validateCurrentStep()) return;
      }
      currentStep = Math.max(...steps);

      submitting = true;
      error = null;

      if (attachmentConfig?.enabled) {
        const maxFiles = attachmentConfig.max_files || 5;
        if (attachments.length > maxFiles) {
          error = `You can attach at most ${maxFiles} files.`;
          return;
        }
        const oversized = attachments.find(file => file.size > attachmentConfig.max_file_size);
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
    <div class="py-8">
      <AlertBox variant="success" message={successMessage} />
      {#if redirectUrl}
        <p class="mt-3 text-center text-sm" style="color: var(--ds-text-subtle);">
          Redirecting...
        </p>
      {/if}
    </div>
  {:else}
    <form onsubmit={(e) => { e.preventDefault(); isLastStep ? handleSubmit() : goToNextStep(); }}>
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

      <div class="space-y-4">
        {#if hasFieldInCurrentStep('title')}
          {@const titleField = currentStepFields.find(f => f.field_identifier === 'title')}
          <div>
            <Label for="form-title" required={titleField.is_required} class="mb-1.5" color="default">
              {titleField.display_name || t('requestForm.title')}
            </Label>
            <Input
              id="form-title"
              bind:value={formData.title}
              placeholder={t('requestForm.enterTitle')}
              required={titleField.is_required}
            />
            {#if titleField.description}
              <p class="mt-1 text-xs" style="color: var(--ds-text-subtle);">{titleField.description}</p>
            {/if}
          </div>
        {/if}

        {#if hasFieldInCurrentStep('description')}
          {@const descField = currentStepFields.find(f => f.field_identifier === 'description')}
          <div>
            <Label for="form-description" required={descField.is_required} class="mb-1.5" color="default">
              {descField.display_name || t('requestForm.description')}
            </Label>
            <Textarea
              id="form-description"
              bind:value={formData.description}
              rows={4}
              placeholder={t('requestForm.describeRequest')}
              required={descField.is_required}
            />
            {#if descField.description}
              <p class="mt-1 text-xs" style="color: var(--ds-text-subtle);">{descField.description}</p>
            {/if}
          </div>
        {/if}

        {#each currentStepFields.filter(f => f.field_type === 'custom') as field}
          {@const fieldDef = getCustomFieldDefinition(field.field_identifier)}
          {#if fieldDef}
            <div>
              <Label required={field.is_required} class="mb-1.5" color="default">
                {field.display_name || fieldDef.name}
              </Label>
              <CustomFieldRenderer
                field={fieldDef}
                value={customFieldValues[field.field_identifier]}
                onChange={(val) => { customFieldValues[field.field_identifier] = val; }}
                readonly={false}
                required={field.is_required}
                {isDarkMode}
              />
              {#if field.description}
                <p class="mt-1 text-xs" style="color: var(--ds-text-subtle);">{field.description}</p>
              {/if}
            </div>
          {/if}
        {/each}

        {#each currentStepFields.filter(f => f.field_type === 'virtual') as field}
          {@const fieldId = `form-${field.field_identifier}`}
          <div>
            {#if field.virtual_field_type === 'checkbox'}
              <Checkbox
                bind:checked={customFieldValues[field.field_identifier]}
                label={getFieldLabel(field)}
              />
            {:else}
              <Label for={fieldId} required={field.is_required} class="mb-1.5" color="default">
                {getFieldLabel(field)}
              </Label>
              {#if field.virtual_field_type === 'textarea'}
                <Textarea
                  id={fieldId}
                  bind:value={customFieldValues[field.field_identifier]}
                  rows={3}
                  required={field.is_required}
                />
              {:else if field.virtual_field_type === 'select'}
                <NativeSelect
                  id={fieldId}
                  bind:value={customFieldValues[field.field_identifier]}
                  options={parseSelectOptions(field.virtual_field_options)}
                  placeholder={t('requestForm.selectOption')}
                  required={field.is_required}
                />
              {:else}
                <Input
                  id={fieldId}
                  bind:value={customFieldValues[field.field_identifier]}
                  required={field.is_required}
                />
              {/if}
            {/if}
            {#if field.description}
              <p class="mt-1 text-xs" style="color: var(--ds-text-subtle);">{field.description}</p>
            {/if}
          </div>
        {/each}

        {#if isLastStep && attachmentConfig?.enabled}
          <div>
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
      </div>

      <div class="mt-6 flex items-center justify-between border-t pt-4" style="border-color: var(--ds-border);">
        {#if !isFirstStep}
          <Button type="button" variant="default" onclick={goToPrevStep}>Back</Button>
        {:else}
          <div></div>
        {/if}

        <Button type="submit" variant="primary" disabled={submitting} loading={submitting}>
          {isLastStep ? submitButtonText : 'Next'}
        </Button>
      </div>
    </form>
  {/if}
</div>

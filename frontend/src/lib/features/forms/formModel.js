export function getFieldLabel(field) {
  return field.display_name || field.field_label || field.field_name || field.field_identifier;
}

export function buildFormSteps(fields = []) {
  const steps = [...new Set(fields.map((field) => field.step_number || 1))].sort((a, b) => a - b);
  return steps.length > 0 ? steps : [1];
}

export function initializeFormValues(fields = [], initialValues = null) {
  const formData = {
    title: initialValues?.title || '',
    description: initialValues?.description || '',
  };
  const customFieldValues = {};

  for (const field of fields) {
    if (field.field_type !== 'custom' && field.field_type !== 'virtual') continue;

    const initialValue = initialValues?.custom_fields?.[field.field_identifier];
    if (initialValue !== undefined) {
      customFieldValues[field.field_identifier] = initialValue;
    } else if (field.field_type === 'virtual' && field.virtual_field_type === 'checkbox') {
      customFieldValues[field.field_identifier] = false;
    } else {
      customFieldValues[field.field_identifier] = '';
    }
  }

  return { formData, customFieldValues };
}

export function validateFormStep({
  fields = [],
  step,
  formData,
  customFieldValues,
  requiredMessage = (label) => `${label} is required`,
}) {
  const stepFields = fields.filter((field) => (field.step_number || 1) === step);

  for (const field of stepFields) {
    if (!field.is_required) continue;

    let value;
    if (field.field_type === 'default') {
      value = formData[field.field_identifier];
    } else {
      value = customFieldValues[field.field_identifier];
    }

    const missing =
      field.field_type === 'virtual' && field.virtual_field_type === 'checkbox'
        ? value !== true
        : value === undefined ||
          value === null ||
          (typeof value === 'string' && value.trim() === '');

    if (missing) return requiredMessage(getFieldLabel(field));
  }

  return '';
}

export function clampFormStep(steps, requestedStep) {
  const fallback = steps[0] || 1;
  const parsed = Number(requestedStep);
  return steps.includes(parsed) ? parsed : fallback;
}

export function parseFormOptions(options) {
  if (Array.isArray(options)) return options;
  try {
    const parsed = JSON.parse(options || '[]');
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

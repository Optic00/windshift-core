export function retainValuesForType(values, fields) {
  const allowed = new Set();
  for (const field of fields || []) {
    allowed.add(String(field.custom_field_id));
    if (field.field_name) {
      allowed.add(field.field_name);
      allowed.add(field.field_name.toLowerCase());
    }
  }

  return Object.fromEntries(
    Object.entries(values || {}).filter(
      ([key]) => allowed.has(key) || allowed.has(key.toLowerCase())
    )
  );
}

let statusesPromise;

export function normalizeDate(dateString) {
  if (!dateString) return null;
  const date = new Date(dateString);
  return Number.isNaN(date.getTime()) ? null : date;
}

export async function getDoneStatusIds(api) {
  try {
    if (!statusesPromise) {
      statusesPromise = api.statuses.getAll();
    }
    const statuses = await statusesPromise;
    if (!Array.isArray(statuses)) return [];
    return statuses
      .filter((status) => status?.category_name?.toLowerCase().trim() === 'done')
      .map((status) => status.id)
      .filter(Boolean);
  } catch (statusError) {
    console.warn('Failed to load statuses for widget:', statusError);
    return [];
  }
}

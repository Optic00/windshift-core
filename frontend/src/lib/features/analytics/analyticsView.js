export { formatDateOnly } from '../../utils/dateFormatter.js';

export const ANALYTICS_MAX_RANGE_DAYS = 366;
export const ANALYTICS_DEFAULT_RANGE_DAYS = 84;

function dateParts(dateString) {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(dateString || '');
  if (!match) return null;
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const timestamp = Date.UTC(year, month - 1, day);
  const date = new Date(timestamp);
  if (
    date.getUTCFullYear() !== year ||
    date.getUTCMonth() !== month - 1 ||
    date.getUTCDate() !== day
  ) {
    return null;
  }
  return { year, month, day, timestamp };
}

export function localDateString(date = new Date()) {
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, '0');
  const day = String(date.getDate()).padStart(2, '0');
  return `${year}-${month}-${day}`;
}

export function shiftDateString(dateString, deltaDays) {
  const parts = dateParts(dateString);
  if (!parts) return '';
  const shifted = new Date(parts.timestamp);
  shifted.setUTCDate(shifted.getUTCDate() + deltaDays);
  return shifted.toISOString().slice(0, 10);
}

export function defaultAnalyticsRange(now = new Date()) {
  const endDate = localDateString(now);
  return {
    startDate: shiftDateString(endDate, -(ANALYTICS_DEFAULT_RANGE_DAYS - 1)),
    endDate,
  };
}

export function inclusiveDateRangeDays(startDate, endDate) {
  const start = dateParts(startDate);
  const end = dateParts(endDate);
  if (!start || !end) return null;
  return Math.round((end.timestamp - start.timestamp) / 86_400_000) + 1;
}

export function validateAnalyticsRange(startDate, endDate) {
  const days = inclusiveDateRangeDays(startDate, endDate);
  if (days === null) return 'invalid';
  if (days < 1) return 'reversed';
  if (days > ANALYTICS_MAX_RANGE_DAYS) return 'too_long';
  return null;
}

export function formatDayNumber(value) {
  const number = Number(value) || 0;
  return new Intl.NumberFormat(undefined, {
    maximumFractionDigits: number < 10 ? 1 : 0,
  }).format(number);
}

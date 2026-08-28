import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  buildDefaultDashboardLayout,
  getDashboardSectionDisplay,
} from '../services/dashboardWidgetRegistry.js';
import { formatRelativeTime } from '../utils/dateFormatter.js';
import { i18n } from './i18n.svelte.js';

describe('dashboard section localization', () => {
  it('translates untouched default sections', () => {
    const [section] = buildDefaultDashboardLayout().sections;
    const translate = (key) => `translated:${key}`;

    expect(getDashboardSectionDisplay(section, translate)).toEqual({
      title: 'translated:dashboard.sections.yourDay.title',
      subtitle: 'translated:dashboard.sections.yourDay.subtitle',
    });
  });

  it('preserves customized section text', () => {
    const section = {
      ...buildDefaultDashboardLayout().sections[0],
      title: 'My focus',
      subtitle: 'What matters now',
    };

    expect(getDashboardSectionDisplay(section, () => 'translated')).toEqual({
      title: 'My focus',
      subtitle: 'What matters now',
    });
  });
});

describe('locale-aware relative time', () => {
  afterEach(async () => {
    vi.useRealTimers();
    await i18n.setLocale('en');
  });

  it('uses the active application locale', async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-08-28T12:00:00Z'));
    await i18n.setLocale('de');

    expect(formatRelativeTime('2026-08-28T11:39:00Z')).toBe('vor 21 Minuten');
  });

  it('keeps the English fallback behavior', async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-08-28T12:00:00Z'));
    await i18n.setLocale('en');

    expect(formatRelativeTime('2026-08-28T11:39:00Z')).toBe('21 minutes ago');
  });
});

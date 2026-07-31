export const embedStyles = `
:host, .wsf-root { box-sizing: border-box; color-scheme: light dark; font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
*, *::before, *::after { box-sizing: inherit; }
.wsf-root { width: 100%; background: transparent; color: var(--ds-text); }
.wsf-card { background: var(--ds-surface-card); border: 1px solid var(--ds-border); border-radius: 6px; padding: 24px; width: 100%; }
.wsf-title { color: var(--ds-text); font-size: 18px; font-weight: 600; line-height: 1.35; margin: 0 0 4px; }
.wsf-description { color: var(--ds-text-subtle); font-size: 14px; line-height: 1.45; margin: 0 0 24px; }
.wsf-field { margin-bottom: 16px; }
.wsf-label { color: var(--ds-text); display: block; font-size: 14px; font-weight: 600; margin-bottom: 6px; }
.wsf-required { color: var(--ds-text-danger); }
.wsf-help { color: var(--ds-text-subtle); font-size: 12px; line-height: 1.35; margin-top: 4px; }
.wsf-actions { align-items: center; border-top: 1px solid var(--ds-border); display: flex; gap: 8px; justify-content: flex-end; margin-top: 24px; padding-top: 16px; }
.wsf-progress { margin-bottom: 20px; }
.wsf-progress-label { color: var(--ds-text-subtle); display: flex; font-size: 12px; justify-content: space-between; line-height: 16px; margin-bottom: 6px; }
.wsf-loading { color: var(--ds-text-subtle); padding: 24px; text-align: center; }
.wsf-multiselect { background: var(--ds-background-input); border: 1px solid var(--ds-border); border-radius: 4px; display: grid; gap: 10px; padding: 12px; }
.wsf-error { margin-bottom: 16px; }
@media (max-width: 520px) {
  .wsf-card { padding: 20px 16px; }
}
`;

export const embedStyles = `
:host, .wsf-root { box-sizing: border-box; color-scheme: light dark; font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
*, *::before, *::after { box-sizing: inherit; }
.wsf-root { --wsf-bg: #fafbfc; --wsf-card: #ffffff; --wsf-text: #172b4d; --wsf-muted: #6b778c; --wsf-border: #091e4224; --wsf-error: #bf2600; --wsf-error-bg: #ffebe6; --wsf-success: #155724; --wsf-success-bg: #dcfce7; --ds-surface: var(--wsf-bg); --ds-surface-raised: var(--wsf-card); --ds-text: var(--wsf-text); --ds-text-subtle: var(--wsf-muted); --ds-text-subtlest: var(--wsf-muted); --ds-text-danger: var(--wsf-error); --ds-border: var(--wsf-border); --ds-border-focused: #2684ff; --ds-border-danger: #ff8f73; --ds-border-success: #4ade80; --ds-icon-danger: #dc2626; --ds-icon-success: #16a34a; --ds-background-input: var(--wsf-bg); --ds-background-neutral: #091e420f; --ds-background-neutral-hovered: #091e4214; --ds-interactive: var(--color-primary-600, #2874bb); --ds-interactive-hovered: var(--color-primary-700, #1d5a94); --ds-interactive-pressed: var(--color-primary-800, #1e3a8a); width: 100%; background: transparent; color: var(--wsf-text); }
.wsf-root[data-theme='dark'] { --wsf-bg: #1d2125; --wsf-card: #282e33; --wsf-text: #f1f2f4; --wsf-muted: #9fadbc; --wsf-border: #a6c5e229; --wsf-error: #ffb8a8; --wsf-error-bg: #42221f; --wsf-success: #b3df72; --wsf-success-bg: #1c3329; --ds-border-danger: #fd9891; --ds-border-success: #7ee2b8; --ds-icon-danger: #f87168; --ds-icon-success: #4bce97; --ds-background-neutral: #a1bdd914; --ds-background-neutral-hovered: #a6c5e229; }
.wsf-card { background: var(--wsf-card); border: 1px solid var(--wsf-border); border-radius: 16px; padding: 24px; width: 100%; }
.wsf-title { font-size: 20px; line-height: 1.25; font-weight: 700; margin: 0 0 6px; color: var(--wsf-text); }
.wsf-description { color: var(--wsf-muted); font-size: 14px; line-height: 1.45; margin: 0 0 20px; }
.wsf-field { margin-bottom: 16px; }
.wsf-label { display: block; color: var(--wsf-text); font-size: 14px; font-weight: 600; margin-bottom: 6px; }
.wsf-required { color: var(--wsf-error); }
.wsf-help { color: var(--wsf-muted); font-size: 12px; line-height: 1.35; margin-top: 4px; }
.wsf-actions { align-items: center; display: flex; gap: 10px; justify-content: flex-end; margin-top: 20px; }
.wsf-progress { margin-bottom: 20px; }
.wsf-progress-label { color: var(--ds-text-subtle); display: flex; font-size: 12px; justify-content: space-between; line-height: 16px; margin-bottom: 6px; }
.wsf-loading { color: var(--wsf-muted); padding: 24px; text-align: center; }
`;

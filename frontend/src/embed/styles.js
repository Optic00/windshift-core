export const embedStyles = `
:host, .wsf-root { box-sizing: border-box; color-scheme: light dark; font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
*, *::before, *::after { box-sizing: inherit; }
.wsf-root { --wsf-brand: #14b8a6; --wsf-bg: #ffffff; --wsf-card: #ffffff; --wsf-text: #0f172a; --wsf-muted: #64748b; --wsf-border: #cbd5e1; --wsf-error: #dc2626; --wsf-error-bg: #fef2f2; --wsf-success: #0f766e; --wsf-success-bg: #f0fdfa; width: 100%; background: transparent; color: var(--wsf-text); }
.wsf-root[data-theme='dark'] { --wsf-bg: #0f172a; --wsf-card: #1e293b; --wsf-text: #f8fafc; --wsf-muted: #94a3b8; --wsf-border: #334155; --wsf-error: #fca5a5; --wsf-error-bg: #450a0a; --wsf-success: #5eead4; --wsf-success-bg: #042f2e; }
.wsf-card { background: var(--wsf-card); border: 1px solid var(--wsf-border); border-radius: 16px; padding: 24px; width: 100%; }
.wsf-title { font-size: 20px; line-height: 1.25; font-weight: 700; margin: 0 0 6px; color: var(--wsf-text); }
.wsf-description { color: var(--wsf-muted); font-size: 14px; line-height: 1.45; margin: 0 0 20px; }
.wsf-field { margin-bottom: 16px; }
.wsf-label { display: block; color: var(--wsf-text); font-size: 14px; font-weight: 600; margin-bottom: 6px; }
.wsf-required { color: var(--wsf-error); }
.wsf-help { color: var(--wsf-muted); font-size: 12px; line-height: 1.35; margin-top: 4px; }
.wsf-input, .wsf-textarea, .wsf-select { background: var(--wsf-bg); border: 1px solid var(--wsf-border); border-radius: 10px; color: var(--wsf-text); display: block; font: inherit; font-size: 14px; min-height: 42px; padding: 10px 12px; width: 100%; }
.wsf-textarea { min-height: 110px; resize: vertical; }
.wsf-input:focus, .wsf-textarea:focus, .wsf-select:focus { border-color: var(--wsf-brand); box-shadow: 0 0 0 3px color-mix(in srgb, var(--wsf-brand) 25%, transparent); outline: none; }
.wsf-checkbox { align-items: center; display: flex; gap: 10px; }
.wsf-checkbox input { accent-color: var(--wsf-brand); height: 18px; width: 18px; }
.wsf-actions { align-items: center; display: flex; gap: 10px; justify-content: flex-end; margin-top: 20px; }
.wsf-button { align-items: center; background: var(--wsf-brand); border: 0; border-radius: 10px; color: #fff; cursor: pointer; display: inline-flex; font: inherit; font-size: 14px; font-weight: 700; justify-content: center; min-height: 42px; padding: 10px 18px; }
.wsf-button:disabled { cursor: not-allowed; opacity: 0.65; }
.wsf-button-secondary { background: transparent; border: 1px solid var(--wsf-border); color: var(--wsf-text); }
.wsf-notice { border-radius: 12px; font-size: 14px; line-height: 1.45; margin-bottom: 16px; padding: 12px 14px; }
.wsf-notice-error { background: var(--wsf-error-bg); color: var(--wsf-error); }
.wsf-notice-success { background: var(--wsf-success-bg); color: var(--wsf-success); }
.wsf-loading { color: var(--wsf-muted); padding: 24px; text-align: center; }
`;

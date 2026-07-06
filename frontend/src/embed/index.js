import { mount, unmount } from 'svelte';
import FormsEmbed from './FormsEmbed.svelte';
import { embedStyles } from './styles.js';

function normalizeBaseUrl(baseUrl) {
  if (!baseUrl) return window.location.origin;
  return String(baseUrl).replace(/\/+$/, '');
}

function getScriptBaseUrl(script) {
  if (!script?.src) return window.location.origin;
  return script.src.replace(/\/embed\/windshift-forms(?:\.es)?\.js(?:[?#].*)?$/, '');
}

function parseDatasetJSON(value, fallback = {}) {
  if (!value) return fallback;
  try {
    return JSON.parse(value);
  } catch (err) {
    console.warn('[Windshift Forms] Ignoring invalid JSON data attribute:', err);
    return fallback;
  }
}

export function mountForm(element, options = {}) {
  if (!element) {
    throw new Error('WindshiftForms.mount requires a target element');
  }
  if (!options.slug) {
    throw new Error('WindshiftForms.mount requires a form channel slug');
  }

  const shadowRoot = element.shadowRoot || element.attachShadow({ mode: 'open' });
  shadowRoot.replaceChildren();

  const style = document.createElement('style');
  style.textContent = embedStyles;
  shadowRoot.appendChild(style);

  const target = document.createElement('div');
  shadowRoot.appendChild(target);

  const app = mount(FormsEmbed, {
    target,
    props: {
      ...options,
      slug: options.slug,
      baseUrl: normalizeBaseUrl(options.baseUrl),
    },
  });

  return {
    unmount: () => {
      unmount(app);
      shadowRoot.replaceChildren();
    },
  };
}

export { mountForm as mount };

const api = { mount: mountForm };

function autoMountFromScript(script) {
  if (!script) return;
  const slug = script.dataset.slug;
  const targetId = script.dataset.target;
  if (!slug || !targetId) return;

  const run = () => {
    const target = document.getElementById(targetId);
    if (!target) {
      console.error(`[Windshift Forms] Target element not found: #${targetId}`);
      return;
    }

    mountForm(target, {
      baseUrl: script.dataset.baseUrl || getScriptBaseUrl(script),
      slug,
      formId: script.dataset.formId ? Number(script.dataset.formId) : undefined,
      theme: script.dataset.theme,
      prefill: parseDatasetJSON(script.dataset.prefill),
    });
  };

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', run, { once: true });
  } else {
    run();
  }
}

autoMountFromScript(document.currentScript);

export default api;

import { mount, unmount } from 'svelte';
import AlertBox from '../lib/components/AlertBox.svelte';
import Button from '../lib/components/Button.svelte';
import Checkbox from '../lib/components/Checkbox.svelte';
import Input from '../lib/components/Input.svelte';
import Label from '../lib/components/Label.svelte';
import NativeSelect from '../lib/components/NativeSelect.svelte';
import Progress from '../lib/components/Progress.svelte';
import Spinner from '../lib/components/Spinner.svelte';
import Textarea from '../lib/components/Textarea.svelte';
import designSystemStyles from './design-system.css?inline';
import FormsEmbed from './FormsEmbed.svelte';
import { embedStyles } from './styles.js';

export const components = Object.freeze({
  AlertBox,
  Button,
  Checkbox,
  Input,
  Label,
  NativeSelect,
  Progress,
  Spinner,
  Textarea,
});

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

function createMountTarget(element) {
  if (!element) {
    throw new Error('WindshiftForms requires a target element');
  }

  const shadowRoot = element.shadowRoot || element.attachShadow({ mode: 'open' });
  shadowRoot.replaceChildren();

  const style = document.createElement('style');
  style.textContent = `${designSystemStyles}\n${embedStyles}`;
  shadowRoot.appendChild(style);

  const target = document.createElement('div');
  shadowRoot.appendChild(target);

  return { shadowRoot, target };
}

export function mountComponent(element, componentName, props = {}) {
  const Component = components[componentName];
  if (!Component) {
    throw new Error(`Unknown Windshift Forms component: ${componentName}`);
  }

  const { shadowRoot, target } = createMountTarget(element);
  const app = mount(Component, { target, props });

  return {
    unmount: () => {
      unmount(app);
      shadowRoot.replaceChildren();
    },
  };
}

export function mountForm(element, options = {}) {
  if (!options.slug) {
    throw new Error('WindshiftForms.mount requires a form channel slug');
  }

  const { shadowRoot, target } = createMountTarget(element);

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

const api = { mount: mountForm, mountComponent, components };

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

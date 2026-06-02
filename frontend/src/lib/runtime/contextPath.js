const GLOBAL_KEY = '__WINDSHIFT_CONTEXT_PATH__';
const INSTALLED_KEY = '__WINDSHIFT_CONTEXT_TRANSLATION_INSTALLED__';

const URL_ATTRS = new Set(['href', 'src', 'action']);
const URL_TAGS = new Set([
  'A',
  'AREA',
  'IMG',
  'IFRAME',
  'SCRIPT',
  'LINK',
  'FORM',
  'SOURCE',
  'VIDEO',
  'AUDIO',
]);

export function contextPath() {
  if (typeof window === 'undefined') return '';
  const raw = /** @type {any} */ (window)[GLOBAL_KEY];
  if (typeof raw !== 'string' || raw === '/' || !raw.startsWith('/')) return '';
  return raw.endsWith('/') ? raw.slice(0, -1) : raw;
}

export function isContextPathEnabled() {
  return contextPath() !== '';
}

export function publicBaseURL() {
  if (typeof window === 'undefined') return contextPath();
  return `${window.location.origin}${contextPath()}`;
}

function isSpecialURL(value) {
  return (
    value === '' ||
    value.startsWith('#') ||
    value.startsWith('mailto:') ||
    value.startsWith('tel:') ||
    value.startsWith('data:') ||
    value.startsWith('blob:') ||
    value.startsWith('javascript:')
  );
}

function hasSameOrigin(url) {
  return typeof window !== 'undefined' && url.origin === window.location.origin;
}

function shouldPrefixPath(pathname) {
  const base = contextPath();
  return (
    base !== '' && pathname.startsWith('/') && !pathname.startsWith(`${base}/`) && pathname !== base
  );
}

export function toExternal(value) {
  const base = contextPath();
  if (!base || typeof value !== 'string' || isSpecialURL(value)) return value;

  if (value.startsWith('//')) return value;

  if (value.startsWith('/')) {
    return shouldPrefixPath(value) ? `${base}${value}` : value;
  }

  try {
    const url = new URL(value, window.location.href);
    if (!hasSameOrigin(url) || !shouldPrefixPath(url.pathname)) return value;
    url.pathname = `${base}${url.pathname}`;
    return url.toString();
  } catch {
    return value;
  }
}

export function toLogical(value) {
  const base = contextPath();
  if (!base || typeof value !== 'string') return value;

  const strip = (pathname) => {
    if (pathname === base) return '/';
    if (pathname.startsWith(`${base}/`)) return pathname.slice(base.length) || '/';
    return pathname;
  };

  if (value.startsWith('/')) {
    return strip(value);
  }

  try {
    const url = new URL(value, window.location.href);
    if (!hasSameOrigin(url)) return value;
    url.pathname = strip(url.pathname);
    return url.pathname + url.search + url.hash;
  } catch {
    return value;
  }
}

function translateRequestInput(input) {
  if (!contextPath()) return input;

  if (typeof input === 'string') return toExternal(input);

  if (input instanceof URL) {
    if (!hasSameOrigin(input) || !shouldPrefixPath(input.pathname)) return input;
    const next = new URL(input.toString());
    next.pathname = `${contextPath()}${next.pathname}`;
    return next;
  }

  if (typeof Request !== 'undefined' && input instanceof Request) {
    const nextURL = toExternal(input.url);
    return nextURL === input.url ? input : new Request(nextURL, input);
  }

  return input;
}

function installFetchTranslation() {
  const originalFetch = window.fetch.bind(window);
  window.fetch = (input, init) => originalFetch(translateRequestInput(input), init);

  if (typeof XMLHttpRequest !== 'undefined') {
    const originalOpen = XMLHttpRequest.prototype.open;
    XMLHttpRequest.prototype.open = function open(method, url, ...rest) {
      return originalOpen.call(
        this,
        method,
        typeof url === 'string' ? toExternal(url) : url,
        ...rest
      );
    };
  }
}

function translateElementAttribute(el, attr) {
  if (!URL_TAGS.has(el.tagName) || !URL_ATTRS.has(attr)) return;
  const value = el.getAttribute(attr);
  if (!value) return;
  const translated = toExternal(value);
  if (translated !== value) {
    el.setAttribute(attr, translated);
  }
}

function translateElement(el) {
  if (!(el instanceof Element)) return;
  for (const attr of URL_ATTRS) translateElementAttribute(el, attr);
  for (const child of el.querySelectorAll(
    'a[href],area[href],img[src],iframe[src],script[src],link[href],form[action],source[src],video[src],audio[src]'
  )) {
    for (const attr of URL_ATTRS) translateElementAttribute(child, attr);
  }
}

function installDOMTranslation() {
  const originalSetAttribute = Element.prototype.setAttribute;
  Element.prototype.setAttribute = function setAttribute(name, value) {
    const nextValue = URL_ATTRS.has(String(name).toLowerCase()) ? toExternal(String(value)) : value;
    return originalSetAttribute.call(this, name, nextValue);
  };

  const observer = new MutationObserver((mutations) => {
    for (const mutation of mutations) {
      if (
        mutation.type === 'attributes' &&
        mutation.target instanceof Element &&
        mutation.attributeName
      ) {
        translateElementAttribute(mutation.target, mutation.attributeName);
      }
      for (const node of mutation.addedNodes) {
        if (node instanceof Element) translateElement(node);
      }
    }
  });

  const start = () => {
    if (document.documentElement) {
      translateElement(document.documentElement);
      observer.observe(document.documentElement, {
        childList: true,
        subtree: true,
        attributes: true,
        attributeFilter: [...URL_ATTRS],
      });
    }
  };

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', start, { once: true });
  } else {
    start();
  }
}

function installWindowOpenTranslation() {
  const originalOpen = window.open.bind(window);
  window.open = (url, target, features) =>
    originalOpen(typeof url === 'string' ? toExternal(url) : url, target, features);

  try {
    const originalAssign = window.location.assign.bind(window.location);
    const originalReplace = window.location.replace.bind(window.location);
    window.location.assign = (url) =>
      originalAssign(typeof url === 'string' ? toExternal(url) : url);
    window.location.replace = (url) =>
      originalReplace(typeof url === 'string' ? toExternal(url) : url);
  } catch {
    // Some browsers expose Location methods as read-only. Direct call sites
    // that navigate to logical root paths are handled explicitly.
  }
}

export function installContextPathTranslation() {
  if (typeof window === 'undefined' || !contextPath()) return;
  if (/** @type {any} */ (window)[INSTALLED_KEY]) return;
  /** @type {any} */ (window)[INSTALLED_KEY] = true;

  installFetchTranslation();
  installDOMTranslation();
  installWindowOpenTranslation();
}

<script>
  /**
   * Compact, color-coded HTTP method pill. Uses --ds-* accents so it
   * picks up the active theme automatically (light + dark).
   */
  let { method = 'get', size = 'sm' } = $props();

  const upper = $derived(String(method).toUpperCase());
  const tone = $derived(toneFor(upper));

  function toneFor(m) {
    switch (m) {
      case 'GET':     return { bg: 'var(--ds-accent-green-subtle)',  fg: 'var(--ds-text-accent-green)'  };
      case 'POST':    return { bg: 'var(--ds-accent-blue-subtle)',   fg: 'var(--ds-text-accent-blue)'   };
      case 'PUT':     return { bg: 'var(--ds-accent-yellow-subtle)', fg: 'var(--ds-text-accent-yellow)' };
      case 'PATCH':   return { bg: 'var(--ds-accent-purple-subtle, var(--ds-accent-yellow-subtle))', fg: 'var(--ds-text-accent-purple, var(--ds-text-accent-yellow))' };
      case 'DELETE':  return { bg: 'var(--ds-danger-subtle)',        fg: 'var(--ds-text-danger)'        };
      case 'HEAD':
      case 'OPTIONS':
      default:        return { bg: 'var(--ds-background-neutral)',   fg: 'var(--ds-text-subtle)'        };
    }
  }
</script>

<span class="method-badge" class:method-badge--md={size === 'md'} style="background-color: {tone.bg}; color: {tone.fg};">
  {upper}
</span>

<style>
  .method-badge {
    display: inline-block;
    padding: 1px 6px;
    border-radius: 4px;
    font-family: var(--ds-font-mono, ui-monospace, monospace);
    font-size: 10.5px;
    font-weight: 600;
    letter-spacing: 0.04em;
    line-height: 1.4;
    text-transform: uppercase;
    min-width: 46px;
    text-align: center;
  }
  .method-badge--md {
    font-size: 12px;
    padding: 2px 8px;
    min-width: 56px;
  }
</style>

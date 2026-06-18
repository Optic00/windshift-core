<script>
  /**
   * Compact work-item status pill. Encapsulates the `${color}1f` (12% alpha)
   * tinted-background convention so the magic alpha + neutral fallback live in
   * one place instead of being hand-rolled per row.
   * @type {{ name?: string | null, color?: string | null }}
   */
  let { name = null, color = null } = $props();
</script>

{#if name}
  <span
    class="status-pill"
    style={color ? `background-color: ${color}1f; color: ${color};` : ''}
    data-testid="status-pill"
  >{name}</span>
{/if}

<style>
  .status-pill {
    display: inline-flex;
    align-items: center;
    border-radius: var(--radius-sm, 4px);
    padding: 1px 8px;
    font-size: 0.75rem;
    font-weight: var(--font-medium, 500);
    white-space: nowrap;
    background-color: var(--ds-background-neutral);
    color: var(--ds-text-subtle);
  }
</style>

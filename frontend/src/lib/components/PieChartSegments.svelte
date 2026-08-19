<script>
  // Renders the ring + segments of a 140x140 donut chart. Intended to be
  // placed inside the consumer's <svg viewBox="0 0 140 140"> so center text
  // and styling stay with the caller.
  let { segments = [], radius = 48, strokeWidth = 16 } = $props();
</script>

<circle
  cx="70"
  cy="70"
  r={radius}
  fill="transparent"
  stroke="var(--ds-border)"
  stroke-width={strokeWidth}
/>
{#each segments as segment (segment.key)}
  <circle
    cx="70"
    cy="70"
    r={radius}
    fill="transparent"
    stroke={segment.color}
    stroke-width={strokeWidth}
    stroke-linecap="butt"
    stroke-dasharray={segment.dasharray}
    stroke-dashoffset={segment.offset}
    transform="rotate(-90 70 70)"
  />
{/each}

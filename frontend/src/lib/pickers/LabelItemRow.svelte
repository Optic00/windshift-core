<script>
  // One row in a label picker dropdown: color dot + name.
  let { label } = $props();
</script>

<div class="flex items-center gap-3 flex-1 min-w-0">
  <span
    class="inline-block w-3 h-3 rounded-full flex-shrink-0"
    style="background-color: {label.color || '#3B82F6'};"
    aria-hidden="true"
  ></span>
  <span class="font-medium text-sm" style="color: var(--ds-text);">
    {label.name}
  </span>
</div>

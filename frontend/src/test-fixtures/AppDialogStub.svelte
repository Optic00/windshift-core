<script>
  let { isOpen = $bindable(false) } = $props();
</script>

{#if isOpen}<div data-testid="dialog-stub"></div>{/if}

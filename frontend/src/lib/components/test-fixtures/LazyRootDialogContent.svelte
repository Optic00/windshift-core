<script>
  let { isOpen = $bindable(false), onaction = undefined } = $props();
</script>

<button
  type="button"
  data-testid="lazy-dialog-content"
  onclick={() => {
    isOpen = false;
    onaction?.();
  }}
>{isOpen ? 'Open' : 'Closed'}</button>

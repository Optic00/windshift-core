<script>
  import AIModalShell from '../components/AIModalShell.svelte';

  let {
    show = $bindable(false),
    title = '',
    icon: Icon = null,
    loading = false,
    error = null,
    onclose = null,
    children,
  } = $props();
</script>

<AIModalShell bind:show {title} icon={Icon} {loading} {error} titleId="ai-modal-title" {onclose} body={children} />

<script>
  import GenericActionListManager from '../features/actions/shared/GenericActionListManager.svelte';

  let {
    actions = [],
    loading = false,
    triggerLabels = {},
    headerTitle = '',
    headerSubtitle = '',
    emptyStateDescription = '',
    oncreate,
    onedit,
    ontoggle,
    ondelete,
    onviewlogs,
    onexecute = null
  } = $props();
</script>

<GenericActionListManager
  {actions}
  {loading}
  {triggerLabels}
  {headerTitle}
  {headerSubtitle}
  {emptyStateDescription}
  {oncreate}
  {onedit}
  {ontoggle}
  {ondelete}
  {onviewlogs}
  {onexecute}
/>
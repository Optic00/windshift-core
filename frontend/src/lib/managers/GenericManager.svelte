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
    onexecute,
    extraActions = null
  } = $props();

  // Merge extra actions into the main oncreate
  let onCreate = $derived(() => {
    if (extraActions) {
      return (action) => {
        oncreate?.(action);
        extraActions?.(action);
      };
    }
    return oncreate;
  });

  // Filter out extra actions from the props passed to GenericActionListManager
  let filteredProps = $derived({
    actions,
    loading,
    triggerLabels,
    headerTitle,
    headerSubtitle,
    emptyStateDescription,
    onedit,
    ontoggle,
    ondelete,
    onviewlogs,
    onexecute
  });
</script>

<GenericActionListManager {...filteredProps} {onCreate} />
<script>
  import { Check, RotateCcw } from '@lucide/svelte';
  import AlertBox from '../../../lib/components/AlertBox.svelte';
  import Button from '../../../lib/components/Button.svelte';
  import Card from '../../../lib/components/Card.svelte';
  import WorkspaceForm from '../../../lib/forms/WorkspaceForm.svelte';
  import StaticViewBackground from '../../../lib/layout/StaticViewBackground.svelte';
  import ViewHeader from '../../../lib/layout/ViewHeader.svelte';

  const initialData = {
    name: 'Product operations',
    key: 'PO',
    description: 'Plan launches, prioritize customer feedback, and keep delivery work visible.',
  };

  let formData = $state({ ...initialData });
  let workspaceFormRef = $state(null);
  let submitted = $state(false);
  let showError = $state(false);

  function handleSubmit() {
    showError = !workspaceFormRef?.validate();
    submitted = !showError;
  }

  function resetForm() {
    workspaceFormRef?.reset();
    formData = { ...initialData };
    submitted = false;
    showError = false;
  }
</script>

<StaticViewBackground contentClass="p-6" testid="design-system-form-example">
  <div class="mx-auto max-w-3xl">
    <div class="mb-8">
      <ViewHeader
        workspaceName="Windshift"
        collection="Composition example"
        viewName="Create workspace"
      />
    </div>

    {#if submitted}
      <AlertBox
        variant="success"
        class="mb-4"
        message="Workspace settings look good. The example was not saved."
      />
    {:else if showError}
      <AlertBox
        variant="error"
        class="mb-4"
        message="Enter a workspace name and key."
      />
    {/if}

    <Card rounded="xl" shadow padding="spacious" class="overflow-visible" dataTestid="design-system-workspace-form">
      {#snippet footer()}
        <div class="flex items-center justify-end gap-2">
          <Button variant="ghost" icon={RotateCcw} onclick={resetForm}>Reset</Button>
          <!-- shortcut-guard-exempt: static design-system composition demo does not register application shortcuts. -->
          <Button
            variant="primary"
            icon={Check}
            dataTestid="design-system-form-submit"
            onclick={handleSubmit}
          >
            Create workspace
          </Button>
        </div>
      {/snippet}

      <WorkspaceForm
        bind:this={workspaceFormRef}
        bind:formData
      />
    </Card>
  </div>
</StaticViewBackground>

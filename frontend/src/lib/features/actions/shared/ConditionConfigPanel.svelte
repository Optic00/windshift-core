<script>
  import Select from '../../../components/Select.svelte';
  import Button from '../../../components/Button.svelte';
  import Input from '../../../components/Input.svelte';

  // Field / operator / value config for a condition node, shared by the
  // action flow editors. The available fields and operators are
  // domain-specific and passed in by the host editor.
  let { selectedNode, store, fields = [], operators = [], onDelete } = $props();
</script>

<div>
  <label for="condition-field" class="block text-xs font-medium mb-1">Field</label>
  <Select
    id="condition-field"
    options={fields}
    value={selectedNode.data?.config?.field_name || ''}
    onchange={(v) =>
      store.updateNodeConfig(selectedNode.id, { field_name: v })}
    size="small"
  />
</div>
<div>
  <label for="condition-operator" class="block text-xs font-medium mb-1">Operator</label>
  <Select
    id="condition-operator"
    options={operators}
    value={selectedNode.data?.config?.operator || 'eq'}
    onchange={(v) =>
      store.updateNodeConfig(selectedNode.id, { operator: v })}
    size="small"
  />
</div>
<div>
  <div class="block text-xs font-medium mb-1">Value</div>
  <Input
    type="text"
    size="small"
    value={selectedNode.data?.config?.value || ''}
    oninput={(e) =>
      store.updateNodeConfig(selectedNode.id, { value: e.currentTarget.value })}
  />
</div>
<Button variant="ghost" size="small" onclick={onDelete}>Delete Node</Button>

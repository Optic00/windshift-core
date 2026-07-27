<script>
  import { t } from '../../stores/i18n.svelte.js';
  import { channelCategoriesStore } from '../../stores/channelCategories.js';
  import Button from '../../components/Button.svelte';
  import Input from '../../components/Input.svelte';
  import Select from '../../components/Select.svelte';
  import Textarea from '../../components/Textarea.svelte';
  import Label from '../../components/Label.svelte';

  let {
    channelFormData = $bindable({
      name: '',
      description: '',
      category_id: null,
    }),
    saving = false,
    onSave = () => {},
    children,
  } = $props();
</script>

<div class="px-16 py-8 max-w-3xl">
  <div class="mb-8">
    <h4 class="text-sm font-semibold mb-4" style="color: var(--ds-text);">{t('channel.basicInformation')}</h4>
    <div class="space-y-4">
      <div class="grid grid-cols-2 gap-4">
        <div>
          <Label color="default" class="mb-2">{t('channel.name')}</Label>
          <Input bind:value={channelFormData.name} placeholder={t('channel.channelName')} />
        </div>
        <div>
          <Label color="default" class="mb-2">{t('channel.category')}</Label>
          <Select
            bind:value={channelFormData.category_id}
            options={[
              { value: null, label: t('channel.noCategory') },
              ...$channelCategoriesStore.map(c => ({ value: c.id, label: c.name })),
            ]}
          />
        </div>
      </div>
      <div>
        <Label color="default" class="mb-2">{t('channel.description')}</Label>
        <Textarea bind:value={channelFormData.description} rows={2} placeholder={t('channel.briefDescription')} />
      </div>
    </div>
  </div>

  {@render children?.()}

  <div class="mt-8 flex justify-end">
    <Button
      onclick={onSave}
      variant="primary"
      disabled={saving}
      dataTestid="channel-save"
    >
      {saving ? t('common.saving') : t('channel.saveChanges')}
    </Button>
  </div>
</div>

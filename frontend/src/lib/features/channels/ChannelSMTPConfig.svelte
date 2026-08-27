<script>
  import { t } from '../../stores/i18n.svelte.js';
  import { api } from '../../api.js';
  import AlertBox from '../../components/AlertBox.svelte';
  import Input from '../../components/Input.svelte';
  import Button from '../../components/Button.svelte';
  import Label from '../../components/Label.svelte';
  import Select from '../../components/Select.svelte';
  import Spinner from '../../components/Spinner.svelte';
  import DescriptionText from '../../components/DescriptionText.svelte';
  import Toggle from '../../components/Toggle.svelte';
  import { isSystemAdmin } from '../../stores/permissions.svelte.js';

  let {
    channelId,
    formData = $bindable({
      host: '',
      port: 587,
      username: '',
      password: '',
      from_email: '',
      from_name: '',
      encryption: 'tls',
      skip_tls_verify: false,
      enabled: false
    }),
    onSave = () => {}
  } = $props();

  let testResult = $state(null);
  let testEmail = $state('');
  let loading = $state(false);

  async function testSmtpSettings() {
    if (!$isSystemAdmin || loading) return;
    if (!channelId || !formData.host || !formData.from_email) {
      testResult = { success: false, message: t('channel.smtpHostAndFromRequired') };
      return;
    }

    if (!testEmail) {
      testResult = { success: false, message: t('channel.testEmailRequired') };
      return;
    }

    testResult = { success: true, message: t('channel.sendingTestEmail'), loading: true };
    loading = true;

    try {
      // Save config first
      const configData = getConfig();
      await api.channels.updateConfig(channelId, configData);

      // Test the channel with test email
      const result = await api.channels.testWithEmail(channelId, testEmail);
      if (result.success) {
        testResult = {
          success: true,
          message: t('channel.testEmailSent'),
          loading: false
        };
        onSave();
      } else {
        testResult = {
          success: false,
          message: `${t('channel.testEmailFailed')}: ${result.message || 'Unknown error'}`,
          loading: false
        };
      }
    } catch (error) {
      console.error('Failed to test SMTP:', error);
      testResult = {
        success: false,
        message: t('channel.testEmailFailed') + ': ' + (error.message || error),
        loading: false
      };
    } finally {
      loading = false;
    }
  }

  export function validate() {
    if (!formData.host?.trim()) {
      return { valid: false, message: t('channel.smtpHostRequired') };
    }
    if (!formData.from_email?.trim()) {
      return { valid: false, message: t('channel.smtpFromEmailRequired') };
    }
    return { valid: true };
  }

  export function getConfig() {
    const plaintext = formData.encryption === 'none';
    return {
      smtp_host: formData.host,
      smtp_port: formData.port || 587,
      smtp_username: plaintext ? '' : formData.username || '',
      smtp_password: plaintext ? '' : formData.password || undefined,
      smtp_from_email: formData.from_email,
      smtp_from_name: formData.from_name || '',
      smtp_encryption: formData.encryption || 'tls',
      smtp_skip_tls_verify: plaintext ? false : formData.skip_tls_verify || false
    };
  }

  export function clearSecret() {
    formData.password = '';
  }
</script>

<div class="pt-6 border-t" style="border-color: var(--ds-border);">
  <h4 class="text-sm font-semibold mb-4" style="color: var(--ds-text);">{t('channel.smtpConfiguration')}</h4>

  <div class="space-y-6">
    <!-- Server Settings -->
    <div class="space-y-4">
      <div class="grid grid-cols-2 gap-4">
        <div>
          <Label color="default" required class="mb-2">{t('channel.smtpHost')}</Label>
          <Input type="text" bind:value={formData.host} required placeholder="smtp.example.com" />
        </div>
        <div>
          <Label color="default" required class="mb-2">{t('channel.smtpPort')}</Label>
          <Input type="number" bind:value={formData.port} required placeholder="587" />
        </div>
      </div>

      <div>
        <Label color="default" class="mb-2">{t('channel.smtpEncryption')}</Label>
        <Select
          id="smtp-encryption"
          bind:value={formData.encryption}
          ariaLabel={t('channel.smtpEncryption')}
          options={[
            { value: 'tls', label: 'STARTTLS (Port 587)' },
            { value: 'ssl', label: 'Implicit TLS (Port 465)' },
            { value: 'none', label: t('channel.noEncryption') }
          ]}
        />
      </div>

      {#if formData.encryption === 'none'}
        <div data-testid="smtp-plaintext-warning">
          <AlertBox variant="warning" message={t('channel.smtpNoEncryptionWarning')} />
        </div>
      {:else}
        <div class="flex items-start justify-between gap-4">
          <div>
            <div class="text-sm font-medium" style="color: var(--ds-text);">
              {t('channel.smtpSkipTlsVerify')}
            </div>
            <DescriptionText>{t('channel.smtpSkipTlsVerifyDescription')}</DescriptionText>
          </div>
          <Toggle bind:checked={formData.skip_tls_verify} dataTestid="smtp-skip-tls-verify" />
        </div>
      {/if}
    </div>

    <!-- Authentication -->
    {#if formData.encryption !== 'none'}
      <div class="pt-4 border-t" style="border-color: var(--ds-border);">
        <h5 class="text-sm font-semibold mb-3" style="color: var(--ds-text);">{t('channel.authentication')}</h5>
        <div class="space-y-4">
          <div>
            <Label color="default" class="mb-2">{t('channel.smtpUsername')}</Label>
            <Input type="text" bind:value={formData.username} placeholder={t('channel.smtpUsernamePlaceholder')} dataTestid="smtp-username" />
          </div>
          <div>
            <Label color="default" class="mb-2">{t('channel.smtpPassword')}</Label>
            <Input type="password" bind:value={formData.password} placeholder={t('channel.secretPlaceholder')} dataTestid="smtp-password" />
            <DescriptionText>
              {t('channel.leaveBlankPassword')}
            </DescriptionText>
          </div>
        </div>
      </div>
    {/if}

    <!-- Sender Settings -->
    <div class="pt-4 border-t" style="border-color: var(--ds-border);">
      <h5 class="text-sm font-semibold mb-3" style="color: var(--ds-text);">{t('channel.senderSettings')}</h5>
      <div class="space-y-4">
        <div>
          <Label color="default" required class="mb-2">{t('channel.smtpFromEmail')}</Label>
          <Input type="email" bind:value={formData.from_email} required placeholder="noreply@example.com" />
        </div>
        <div>
          <Label color="default" class="mb-2">{t('channel.smtpFromName')}</Label>
          <Input type="text" bind:value={formData.from_name} placeholder={t('channel.smtpFromNamePlaceholder')} />
        </div>
      </div>
    </div>

    <div class="flex items-center justify-between pt-4 border-t" style="border-color: var(--ds-border);">
      <div>
        <div class="text-sm font-medium" style="color: var(--ds-text);">
          {t('channel.enableSmtp', 'Enable SMTP Channel')}
        </div>
        <div class="text-xs mt-1" style="color: var(--ds-text-subtle);">
          {formData.enabled
            ? t('channel.smtpIsActive', 'SMTP channel is active')
            : t('channel.smtpIsInactive', 'SMTP channel is currently disabled')}
        </div>
      </div>
      <Toggle bind:checked={formData.enabled} />
    </div>
  </div>

  {#if $isSystemAdmin}
    <!-- Test SMTP Section -->
    <div class="mt-6 pt-6 border-t" style="border-color: var(--ds-border);">
      <h5 class="text-sm font-semibold mb-4" style="color: var(--ds-text);">{t('channel.testSmtp')}</h5>
      <div class="space-y-4">
        <div>
          <Label color="default" class="mb-2">{t('channel.testEmailAddress')}</Label>
          <Input type="email" bind:value={testEmail} placeholder={t('channel.testEmailPlaceholder')} />
        </div>
        <Button onclick={testSmtpSettings} variant="secondary" disabled={!formData.host || !formData.from_email || loading}>
          {t('channel.sendTestEmail')}
        </Button>
      </div>

      {#if testResult}
        {#if testResult.loading}
          <div class="mt-4 flex items-center gap-2 text-sm" style="color: var(--ds-text-subtle);">
            <Spinner size="sm" />
            <span>{testResult.message}</span>
          </div>
        {:else}
          <AlertBox
            variant={testResult.success ? 'success' : 'error'}
            message={testResult.message}
            class="mt-4"
          />
        {/if}
      {/if}
    </div>
  {/if}
</div>

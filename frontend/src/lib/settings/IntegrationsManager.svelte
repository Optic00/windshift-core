<script>
	// OAuth integrations share a page: outbound providers connect Windshift to
	// external apps, while inbound clients authorize apps to mint user tokens.

	import { onMount } from 'svelte';
	import Tabs from '../components/Tabs.svelte';
	import IntegrationProviderManager from './IntegrationProviderManager.svelte';
	import OAuthClientManager from './OAuthClientManager.svelte';
	import ZammadConnectionManager from './ZammadConnectionManager.svelte';
	import NetBoxConnectionManager from './NetBoxConnectionManager.svelte';
	import { ArrowUpRight, ArrowDownLeft, TicketCheck, Server } from '@lucide/svelte';
	import { t } from '../stores/i18n.svelte.js';

	let activeTab = $state('outbound');

	onMount(() => {
		const requested = new URLSearchParams(window.location.search).get('tab');
		if (requested === 'zammad' || requested === 'netbox') activeTab = requested;
	});

	const tabs = $derived([
		{ id: 'outbound', label: t('integrations.directions.outbound'), icon: ArrowUpRight },
		{ id: 'inbound', label: t('integrations.directions.inbound'), icon: ArrowDownLeft },
		{ id: 'zammad', label: t('zammad.tab'), icon: TicketCheck },
		{ id: 'netbox', label: t('netbox.tab'), icon: Server },
	]);
</script>

<div class="space-y-4">
	<Tabs {tabs} bind:activeTab>
		{#if activeTab === 'outbound'}
			<p class="text-sm mb-4" style="color: var(--ds-text-subtle);">
				{t('integrations.directions.outboundDescription')}
			</p>
			<IntegrationProviderManager />
		{:else if activeTab === 'inbound'}
			<p class="text-sm mb-4" style="color: var(--ds-text-subtle);">
				{t('integrations.directions.inboundDescription')}
			</p>
			<OAuthClientManager />
		{:else if activeTab === 'zammad'}
			<ZammadConnectionManager />
		{:else if activeTab === 'netbox'}
			<NetBoxConnectionManager />
		{/if}
	</Tabs>
</div>

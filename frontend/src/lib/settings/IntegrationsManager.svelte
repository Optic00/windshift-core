<script>
	// OAuth integrations share a page: outbound providers connect Windshift to
	// external apps, while inbound clients authorize apps to mint user tokens.

	import Tabs from '../components/Tabs.svelte';
	import IntegrationProviderManager from './IntegrationProviderManager.svelte';
	import OAuthClientManager from './OAuthClientManager.svelte';
	import { ArrowUpRight, ArrowDownLeft } from '@lucide/svelte';

	let activeTab = $state('outbound');

	const tabs = [
		{ id: 'outbound', label: 'Outbound', icon: ArrowUpRight },
		{ id: 'inbound', label: 'Inbound', icon: ArrowDownLeft },
	];
</script>

<div class="space-y-4">
	<Tabs {tabs} bind:activeTab>
		{#if activeTab === 'outbound'}
			<p class="text-sm mb-4" style="color: var(--ds-text-subtle);">
				Apps that <strong>Windshift connects to</strong>. Add the OAuth credentials Windshift uses to read data from external services like Notion or Confluence.
			</p>
			<IntegrationProviderManager />
		{:else if activeTab === 'inbound'}
			<p class="text-sm mb-4" style="color: var(--ds-text-subtle);">
				Apps that <strong>connect to Windshift</strong> on a user's behalf. Register a third-party app once; users then authorize it via OAuth 2.0 (authorization code + PKCE) to mint per-user Windshift API tokens.
			</p>
			<OAuthClientManager />
		{/if}
	</Tabs>
</div>

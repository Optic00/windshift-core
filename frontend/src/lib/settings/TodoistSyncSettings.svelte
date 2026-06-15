<script>
	import { onMount } from 'svelte';
	import { api } from '../api.js';
	import { Loader2, RefreshCw } from '@lucide/svelte';
	import Button from '../components/Button.svelte';
	import { formatDateSimple } from '../utils/dateFormatter.js';

	let loading = $state(true);
	let saving = $state(false);
	let syncing = $state(false);
	let err = $state('');
	let syncMsg = $state('');

	// Local, editable copy of the server config.
	let enabled = $state(false);
	let scopeMode = $state('all');
	let projectId = $state('');
	let lastSyncedAt = $state(null);
	let lastError = $state('');

	let projects = $state([]);
	let projectsLoaded = $state(false);
	let loadingProjects = $state(false);

	onMount(load);

	async function load() {
		loading = true;
		err = '';
		try {
			const s = await api.todoistSync.get();
			enabled = !!s.enabled;
			scopeMode = s.scope_mode || 'all';
			projectId = s.todoist_project_id || '';
			lastSyncedAt = s.last_synced_at || null;
			lastError = s.last_error || '';
			if (scopeMode === 'project') await loadProjects();
		} catch (e) {
			console.error('Failed to load Todoist sync settings:', e);
			err = 'Failed to load sync settings';
		} finally {
			loading = false;
		}
	}

	async function loadProjects() {
		if (projectsLoaded || loadingProjects) return;
		loadingProjects = true;
		try {
			projects = (await api.todoistSync.getProjects()) || [];
			projectsLoaded = true;
		} catch (e) {
			console.error('Failed to load Todoist projects:', e);
			err = 'Could not load your Todoist projects';
		} finally {
			loadingProjects = false;
		}
	}

	async function save() {
		// A 'project' scope is only valid once a project is picked.
		if (scopeMode === 'project' && !projectId) return;
		saving = true;
		err = '';
		try {
			const s = await api.todoistSync.update({
				enabled,
				scope_mode: scopeMode,
				todoist_project_id: scopeMode === 'project' ? projectId : '',
			});
			enabled = !!s.enabled;
			scopeMode = s.scope_mode || 'all';
			projectId = s.todoist_project_id || '';
			lastSyncedAt = s.last_synced_at || null;
			lastError = s.last_error || '';
		} catch (e) {
			console.error('Failed to save Todoist sync settings:', e);
			err = e?.message || 'Failed to save sync settings';
		} finally {
			saving = false;
		}
	}

	async function toggleEnabled() {
		enabled = !enabled;
		await save();
	}

	async function onScopeChange(value) {
		scopeMode = value;
		if (scopeMode === 'project') {
			await loadProjects();
			if (!projectId) return; // wait for a project to be chosen
		}
		await save();
	}

	async function onProjectChange(value) {
		projectId = value;
		await save();
	}

	async function runNow() {
		syncing = true;
		syncMsg = '';
		err = '';
		try {
			const r = await api.todoistSync.run();
			const into = (r.created_in_ws || 0) + (r.updated_in_ws || 0) + (r.deleted_in_ws || 0);
			const out = (r.created_in_td || 0) + (r.updated_in_td || 0) + (r.deleted_in_td || 0);
			syncMsg = r.ok
				? `Synced — ${into} change(s) in, ${out} out`
				: `Synced with issues: ${r.error || 'unknown error'}`;
			await load();
		} catch (e) {
			console.error('Manual Todoist sync failed:', e);
			err = e?.message || 'Sync failed';
		} finally {
			syncing = false;
		}
	}
</script>

<div
	class="mt-3 pt-3 border-t"
	style="border-color: var(--ds-border);"
	data-testid="todoist-sync-settings"
>
	{#if loading}
		<div class="flex items-center gap-2 text-sm" style="color: var(--ds-text-subtle);">
			<Loader2 class="w-4 h-4 animate-spin" />
			Loading sync settings…
		</div>
	{:else}
		<div class="flex items-center justify-between gap-3">
			<div class="min-w-0">
				<div class="text-sm font-medium" style="color: var(--ds-text);">
					Sync personal tasks
				</div>
				<p class="text-xs mt-0.5" style="color: var(--ds-text-subtle);">
					Two-way sync between your personal task list and Todoist.
				</p>
			</div>
			<label class="flex items-center gap-2 shrink-0 cursor-pointer">
				<input
					type="checkbox"
					checked={enabled}
					disabled={saving}
					onchange={toggleEnabled}
					data-testid="todoist-sync-toggle"
				/>
				<span class="text-sm" style="color: var(--ds-text-subtle);">
					{enabled ? 'On' : 'Off'}
				</span>
			</label>
		</div>

		{#if enabled}
			<div class="mt-3 flex flex-col gap-3">
				<div class="flex items-center gap-2 flex-wrap">
					<span class="text-sm" style="color: var(--ds-text-subtle);">Sync</span>
					<select
						value={scopeMode}
						disabled={saving}
						onchange={(e) => onScopeChange(e.currentTarget.value)}
						class="text-sm rounded px-2 py-1 border"
						style="border-color: var(--ds-border); background-color: var(--ds-surface); color: var(--ds-text);"
						data-testid="todoist-sync-scope"
					>
						<option value="all">Everything</option>
						<option value="project">A single project</option>
					</select>

					{#if scopeMode === 'project'}
						{#if loadingProjects}
							<Loader2 class="w-4 h-4 animate-spin" style="color: var(--ds-text-subtle);" />
						{:else}
							<select
								value={projectId}
								disabled={saving}
								onchange={(e) => onProjectChange(e.currentTarget.value)}
								class="text-sm rounded px-2 py-1 border"
								style="border-color: var(--ds-border); background-color: var(--ds-surface); color: var(--ds-text);"
								data-testid="todoist-sync-project"
							>
								<option value="" disabled>Choose a project…</option>
								{#each projects as p}
									<option value={p.id}>{p.name}</option>
								{/each}
							</select>
						{/if}
					{/if}
				</div>

				<div class="flex items-center gap-3 flex-wrap">
					<Button
						variant="secondary"
						size="small"
						onclick={runNow}
						disabled={syncing}
						dataTestid="todoist-sync-now"
					>
						{#if syncing}
							<Loader2 class="w-4 h-4 animate-spin mr-1" />
							Syncing…
						{:else}
							<RefreshCw class="w-4 h-4 mr-1" />
							Sync now
						{/if}
					</Button>
					<span class="text-xs" style="color: var(--ds-text-subtlest);" data-testid="todoist-sync-status">
						{#if lastSyncedAt}
							Last synced {formatDateSimple(lastSyncedAt)}
						{:else}
							Not synced yet
						{/if}
					</span>
				</div>

				{#if syncMsg}
					<p class="text-xs" style="color: var(--ds-text-subtle);">{syncMsg}</p>
				{/if}
				{#if lastError}
					<p class="text-xs" style="color: var(--ds-text-danger);">Last error: {lastError}</p>
				{/if}
			</div>
		{/if}

		{#if err}
			<p class="text-xs mt-2" style="color: var(--ds-text-danger);">{err}</p>
		{/if}
	{/if}
</div>

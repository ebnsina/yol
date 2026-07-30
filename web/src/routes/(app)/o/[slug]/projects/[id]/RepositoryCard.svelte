<script lang="ts">
	import { ApiError } from '$lib/api/client';
	import { codeApi, projectsApi } from '$lib/api/projects';
	import type { Installation, Project, Repository } from '$lib/api/types';
	import { Alert, Badge, Button, Card, Field, Icon, Select, Spinner, toast } from '$lib/ui';
	import { GithubIcon } from '@hugeicons/core-free-icons';

	interface Props {
		slug: string;
		project: Project;
		onconnected: () => void;
	}

	let { slug, project, onconnected }: Props = $props();

	let installUrl = $state('');
	let installations = $state<Installation[] | null>(null);
	let repositories = $state<Repository[] | null>(null);
	let chosenInstallation = $state('');
	let chosenRepository = $state('');
	let loadError = $state<string | undefined>();
	let working = $state(false);
	let changing = $state(false);

	// Worked out here rather than inside the markup, because what a snippet knows about a value
	// being present does not follow it inside.
	let accountOptions = $derived(
		(installations ?? []).map((installation) => ({
			value: installation.id,
			label: installation.account
		}))
	);
	let repositoryOptions = $derived(
		(repositories ?? []).map((repository) => ({
			value: String(repository.id),
			label: repository.private ? `${repository.fullName} (private)` : repository.fullName
		}))
	);

	async function load() {
		try {
			const overview = await codeApi.overview(slug);
			installUrl = overview.installUrl;
			installations = overview.installations;

			if (installations.length > 0 && !chosenInstallation) {
				chosenInstallation = installations[0].id;
				await loadRepositories();
			}
		} catch (error) {
			loadError =
				error instanceof ApiError ? error.message : 'Something went wrong. Please try again.';
		}
	}

	async function loadRepositories() {
		repositories = null;
		try {
			repositories = await codeApi.repositories(slug, chosenInstallation);
			chosenRepository = repositories[0] ? String(repositories[0].id) : '';
		} catch (error) {
			loadError =
				error instanceof ApiError ? error.message : 'We could not list your repositories.';
		}
	}

	async function connect() {
		const repository = repositories?.find((r) => String(r.id) === chosenRepository);
		if (!repository) return;

		working = true;
		try {
			await projectsApi.connectRepository(slug, project.id, {
				installationId: chosenInstallation,
				repositoryId: String(repository.id),
				fullName: repository.fullName
			});
			toast.success(`${repository.fullName} is connected.`);
			changing = false;
			onconnected();
		} catch (error) {
			toast.error(error instanceof ApiError ? error.message : 'Something went wrong.');
		} finally {
			working = false;
		}
	}

	$effect(() => {
		void load();
	});
</script>

<Card title="Code" description="Where this project builds from.">
	{#snippet actions()}
		{#if project.repository && project.permissions.manage && !changing}
			<Button size="sm" variant="secondary" onclick={() => (changing = true)}>Change</Button>
		{/if}
	{/snippet}

	<div class="flex flex-col gap-4">
		{#if loadError}
			<Alert tone="danger" title={loadError} />
		{/if}

		{#if project.repository && !changing}
			<div class="flex items-center gap-2 text-sm">
				<Icon icon={GithubIcon} size={16} />
				<span class="font-mono">{project.repository.fullName}</span>
			</div>
			<p class="text-xs text-ink-muted">
				Pushing to a branch an environment follows deploys it. Nothing else is watched.
			</p>
		{:else if !installations}
			<div class="flex justify-center py-6"><Spinner /></div>
		{:else if installations.length === 0}
			<div class="flex flex-col items-start gap-3">
				<p class="text-sm text-ink-muted">
					Install our app on GitHub to give this organization access to your repositories. You
					choose which ones.
				</p>
				<Button href={installUrl}>
					<Icon icon={GithubIcon} size={14} />
					Install on GitHub
				</Button>
				<p class="text-xs text-ink-subtle">Come back to this page once you have.</p>
			</div>
		{:else}
			<div class="flex flex-col gap-4">
				{#if accountOptions.length > 1}
					<Field label="Account" id="installation">
						{#snippet children({ id })}
							<Select
								{id}
								bind:value={chosenInstallation}
								onchange={loadRepositories}
								options={accountOptions}
							/>
						{/snippet}
					</Field>
				{/if}

				{#if !repositories}
					<div class="py-2"><Spinner /></div>
				{:else if repositories.length === 0}
					<p class="text-sm text-ink-muted">
						That installation has access to no repositories yet. Grant access on GitHub, then reload
						this page.
					</p>
				{:else}
					<Field label="Repository" id="repository">
						{#snippet children({ id })}
							<Select {id} bind:value={chosenRepository} options={repositoryOptions} />
						{/snippet}
					</Field>
				{/if}

				<div class="flex items-center gap-3">
					<Button onclick={connect} loading={working} disabled={!chosenRepository}>
						Connect repository
					</Button>
					{#if changing}
						<Button variant="ghost" onclick={() => (changing = false)}>Cancel</Button>
					{/if}
					<a href={installUrl} class="text-xs text-ink-subtle hover:text-ink">
						Change what we can see on GitHub
					</a>
				</div>
			</div>
		{/if}
	</div>
</Card>

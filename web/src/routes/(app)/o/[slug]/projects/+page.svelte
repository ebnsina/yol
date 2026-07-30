<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { ApiError } from '$lib/api/client';
	import { organizationsApi } from '$lib/api/organizations';
	import { projectsApi } from '$lib/api/projects';
	import type { Organization, Project } from '$lib/api/types';
	import { formatRelative } from '$lib/format';
	import { Alert, Badge, Button, Card, EmptyState, Field, Icon, Input, Spinner } from '$lib/ui';
	import {
		ArrowRight01Icon,
		FolderLibraryIcon,
		GithubIcon,
		PlusSignIcon
	} from '@hugeicons/core-free-icons';

	let slug = $derived(page.params.slug!);

	let organization = $state<Organization | null>(null);
	let projects = $state<Project[] | null>(null);
	let loadError = $state<string | undefined>();

	let creating = $state(false);
	let name = $state('');
	let submitting = $state(false);
	let formError = $state<string | undefined>();
	let fieldErrors = $state<Record<string, string>>({});

	async function load() {
		try {
			[organization, projects] = await Promise.all([
				organizationsApi.get(slug),
				projectsApi.list(slug)
			]);
		} catch (error) {
			loadError =
				error instanceof ApiError ? error.message : 'Something went wrong. Please try again.';
		}
	}

	async function submit(event: SubmitEvent) {
		event.preventDefault();
		if (submitting) return;

		formError = undefined;
		fieldErrors = {};
		if (!name.trim()) {
			fieldErrors = { name: 'Give this project a name.' };
			return;
		}

		submitting = true;
		try {
			const created = await projectsApi.create(slug, name.trim());
			await goto(`/o/${slug}/projects/${created.id}`);
		} catch (error) {
			if (error instanceof ApiError) {
				fieldErrors = error.fields;
				formError = Object.keys(error.fields).length === 0 ? error.message : undefined;
			} else {
				formError = 'Something went wrong. Please try again.';
			}
		} finally {
			submitting = false;
		}
	}

	$effect(() => {
		void load();
	});
</script>

<svelte:head><title>Projects · yol</title></svelte:head>

<div class="flex flex-col gap-6">
	<header class="flex items-end justify-between gap-4">
		<div class="flex flex-col gap-1">
			<a href={`/o/${slug}`} class="text-xs text-ink-subtle hover:text-ink">
				← {organization?.name ?? 'Organization'}
			</a>
			<h1 class="text-xl font-semibold tracking-tight">Projects</h1>
			<p class="text-sm text-ink-muted">
				Each project deploys to your own servers. Production and staging come with it.
			</p>
		</div>
		{#if projects?.length && organization?.permissions.manageServers}
			<Button size="sm" onclick={() => (creating = true)}>
				<Icon icon={PlusSignIcon} size={14} />
				New project
			</Button>
		{/if}
	</header>

	{#if loadError}
		<Alert tone="danger" title={loadError} />
	{/if}

	{#if !projects && !loadError}
		<div class="flex justify-center py-16"><Spinner /></div>
	{:else if organization && !organization.permissions.manageServers && projects?.length === 0}
		<Card>
			<EmptyState
				icon={FolderLibraryIcon}
				title="No projects yet"
				description="Ask an owner or admin of this organization to create one."
			/>
		</Card>
	{:else if creating || projects?.length === 0}
		<Card
			title="New project"
			description="A name is all that is needed. You can connect a repository next."
		>
			<form onsubmit={submit} class="flex flex-col gap-4">
				{#if formError}
					<Alert tone="danger" title={formError} />
				{/if}

				<Field label="Name" id="project-name" error={fieldErrors.name}>
					{#snippet children({ id, describedBy, invalid })}
						<Input
							{id}
							{describedBy}
							{invalid}
							bind:value={name}
							placeholder="Shop"
							autocomplete="off"
						/>
					{/snippet}
				</Field>

				<div class="flex items-center gap-3">
					<Button type="submit" loading={submitting}>Create project</Button>
					{#if projects?.length}
						<Button variant="ghost" type="button" onclick={() => (creating = false)}>Cancel</Button>
					{/if}
				</div>
			</form>
		</Card>
	{/if}

	{#if projects?.length}
		<div class="flex flex-col">
			{#each projects as project (project.id)}
				<a
					href={`/o/${slug}/projects/${project.id}`}
					class="flex items-center justify-between gap-4 border border-line px-5 py-4 not-first:border-t-0 hover:bg-surface-raised"
				>
					<div class="flex items-center gap-3">
						<span class="border border-line bg-surface-sunken p-2 text-ink-subtle">
							<Icon icon={FolderLibraryIcon} size={16} />
						</span>
						<div class="flex flex-col gap-1">
							<span class="font-medium">{project.name}</span>
							<span class="flex items-center gap-2 text-xs text-ink-muted">
								{#if project.repository}
									<Icon icon={GithubIcon} size={12} />
									<span class="font-mono">{project.repository.fullName}</span>
								{:else}
									No repository connected yet
								{/if}
							</span>
						</div>
					</div>

					<div class="flex items-center gap-4">
						<span class="text-right numeric text-xs text-ink-subtle">
							{formatRelative(project.createdAt)}
						</span>
						<Icon icon={ArrowRight01Icon} size={16} class="text-ink-subtle" />
					</div>
				</a>
			{/each}
		</div>
	{/if}
</div>

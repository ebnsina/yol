<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { ApiError } from '$lib/api/client';
	import { projectsApi } from '$lib/api/projects';
	import { serversApi } from '$lib/api/servers';
	import type { Project, Server } from '$lib/api/types';
	import { Alert, Button, Spinner, toast } from '$lib/ui';
	import EnvironmentCard from './EnvironmentCard.svelte';
	import RepositoryCard from './RepositoryCard.svelte';

	let slug = $derived(page.params.slug!);
	let projectId = $derived(page.params.id!);

	let project = $state<Project | null>(null);
	let servers = $state<Server[]>([]);
	let loadError = $state<string | undefined>();
	let removing = $state(false);

	async function load() {
		try {
			[project, servers] = await Promise.all([
				projectsApi.get(slug, projectId),
				serversApi.list(slug)
			]);
		} catch (error) {
			loadError =
				error instanceof ApiError ? error.message : 'Something went wrong. Please try again.';
		}
	}

	async function remove() {
		if (!project) return;
		removing = true;
		try {
			await projectsApi.remove(slug, project.id);
			toast.success(`${project.name} was deleted.`);
			await goto(`/o/${slug}/projects`);
		} catch (error) {
			toast.error(error instanceof ApiError ? error.message : 'Something went wrong.');
		} finally {
			removing = false;
		}
	}

	// Only servers we manage can be deployed to; a watched one is somebody else's arrangement.
	let deployable = $derived(servers.filter((server) => server.mode === 'managed'));

	$effect(() => {
		void load();
	});
</script>

<svelte:head><title>{project?.name ?? 'Project'} · yol</title></svelte:head>

{#if loadError}
	<Alert tone="danger" title={loadError} />
{:else if !project}
	<div class="flex justify-center py-16"><Spinner /></div>
{:else}
	<div class="flex flex-col gap-6">
		<header class="flex items-end justify-between gap-4">
			<div class="flex flex-col gap-1">
				<a href={`/o/${slug}/projects`} class="text-xs text-ink-subtle hover:text-ink">
					← Projects
				</a>
				<h1 class="text-xl font-semibold tracking-tight">{project.name}</h1>
				<p class="text-sm text-ink-muted">
					Deployed to servers you own. Pushing to a branch deploys the environment following it.
				</p>
			</div>
		</header>

		<RepositoryCard {slug} {project} onconnected={load} />

		{#if deployable.length === 0}
			<Alert tone="warning" title="No servers to deploy to">
				Connect a server to this organization first.
			</Alert>
		{/if}

		{#each project.environments ?? [] as environment (environment.id)}
			<EnvironmentCard {slug} {project} {environment} servers={deployable} onchanged={load} />
		{/each}

		{#if project.permissions.manage}
			<div class="flex items-center justify-between gap-4 border border-line px-5 py-4">
				<div class="flex flex-col gap-1">
					<span class="text-sm font-medium">Delete this project</span>
					<span class="text-xs text-ink-muted">
						Its containers are removed from your servers on the next pass. Volumes are left alone.
					</span>
				</div>
				<Button variant="danger" onclick={remove} loading={removing}>Delete</Button>
			</div>
		{/if}
	</div>
{/if}

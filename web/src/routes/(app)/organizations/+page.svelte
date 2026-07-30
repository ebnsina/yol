<script lang="ts">
	import { goto } from '$app/navigation';
	import { organizationsApi } from '$lib/api/organizations';
	import { OrganizationNameSchema } from '$lib/api/schemas';
	import type { Organization } from '$lib/api/types';
	import { ApiError } from '$lib/api/client';
	import { createForm } from '$lib/form.svelte';
	import { formatRelative } from '$lib/format';
	import { Alert, Badge, Button, Card, Field, Icon, Input, Spinner } from '$lib/ui';
	import { ArrowRight01Icon, PlusSignIcon } from '@hugeicons/core-free-icons';

	let organizations = $state<Organization[] | null>(null);
	let loadError = $state<string | undefined>();
	let creating = $state(false);
	let name = $state('');

	const form = createForm({
		schema: OrganizationNameSchema,
		submit: (values) => organizationsApi.create(values.name),
		onSuccess: async (created) => {
			await goto(`/o/${created.slug}`);
		}
	});

	async function load() {
		try {
			organizations = await organizationsApi.list();
		} catch (error) {
			loadError =
				error instanceof ApiError ? error.message : 'Something went wrong. Please try again.';
		}
	}

	$effect(() => {
		void load();
	});
</script>

<svelte:head><title>Organizations · yol</title></svelte:head>

<div class="flex flex-col gap-6">
	<header class="flex items-end justify-between gap-4">
		<div class="flex flex-col gap-1">
			<h1 class="text-xl font-semibold tracking-tight">Organizations</h1>
			<p class="text-sm text-ink-muted">
				Servers, projects and people live inside an organization.
			</p>
		</div>
		{#if organizations?.length}
			<Button size="sm" onclick={() => (creating = true)}>
				<Icon icon={PlusSignIcon} size={14} />
				New organization
			</Button>
		{/if}
	</header>

	{#if loadError}
		<Alert tone="danger" title={loadError} />
	{/if}

	{#if creating || organizations?.length === 0}
		<Card
			title="Create an organization"
			description={organizations?.length === 0
				? 'You have no organizations yet. Create one to get started.'
				: 'You can rename it later; the web address stays the same.'}
		>
			{#if form.formError}
				<div class="mb-4"><Alert tone="danger" title={form.formError} /></div>
			{/if}

			<form
				class="flex flex-col gap-4"
				onsubmit={(event) => {
					event.preventDefault();
					form.handleSubmit({ name });
				}}
			>
				<div class="max-w-sm">
					<Field label="Name" id="org-name" error={form.fieldErrors.name}>
						{#snippet children({ id, describedBy, invalid })}
							<Input
								{id}
								{describedBy}
								{invalid}
								bind:value={name}
								placeholder="Acme Engineering"
								oninput={() => form.clearField('name')}
							/>
						{/snippet}
					</Field>
				</div>

				<div class="flex items-center gap-2">
					<Button type="submit" size="sm" loading={form.submitting}>Create organization</Button>
					{#if organizations?.length}
						<Button
							type="button"
							size="sm"
							variant="secondary"
							onclick={() => {
								creating = false;
								form.clear();
							}}
						>
							Cancel
						</Button>
					{/if}
				</div>
			</form>
		</Card>
	{/if}

	{#if organizations === null}
		<div class="flex justify-center py-14 text-ink-subtle">
			<Spinner size={20} />
		</div>
	{:else if organizations.length > 0}
		<ul class="flex flex-col">
			{#each organizations as organization (organization.id)}
				<li>
					<a
						href={`/o/${organization.slug}`}
						class="flex items-center justify-between gap-4 border border-b-0 border-line bg-surface px-5 py-4 last:border-b hover:bg-surface-sunken"
					>
						<div class="flex flex-col gap-1">
							<span class="text-sm font-medium text-ink">{organization.name}</span>
							<span class="text-xs text-ink-subtle">
								Created {formatRelative(organization.createdAt)}
							</span>
						</div>
						<div class="flex items-center gap-3">
							<Badge tone={organization.role === 'owner' ? 'strong' : 'neutral'}>
								{organization.role}
							</Badge>
							<Icon icon={ArrowRight01Icon} size={16} class="text-ink-subtle" />
						</div>
					</a>
				</li>
			{/each}
		</ul>
	{/if}
</div>

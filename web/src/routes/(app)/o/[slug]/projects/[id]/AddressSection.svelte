<script lang="ts">
	import { ApiError } from '$lib/api/client';
	import { projectsApi } from '$lib/api/projects';
	import type { Address, Project } from '$lib/api/types';
	import { formatRelative } from '$lib/format';
	import { Badge, Button, Field, Icon, Input, Spinner, toast } from '$lib/ui';
	import {
		CheckmarkCircle01Icon,
		Copy01Icon,
		Delete02Icon,
		GlobalIcon,
		LinkSquare02Icon,
		LockKeyIcon
	} from '@hugeicons/core-free-icons';

	interface Props {
		slug: string;
		projectId: string;
		environmentId: string;
		project: Project;
	}

	let { slug, projectId, environmentId, project }: Props = $props();

	let address = $state<Address | null>(null);
	let hostname = $state('');
	let adding = $state(false);
	let verifying = $state<string | undefined>();
	let addError = $state<string | undefined>();

	async function load() {
		try {
			address = await projectsApi.address(slug, projectId, environmentId);
		} catch (error) {
			toast.error(error instanceof ApiError ? error.message : 'Something went wrong.');
		}
	}

	async function add(event: SubmitEvent) {
		event.preventDefault();
		addError = undefined;
		if (!hostname.trim()) {
			addError = 'Enter a hostname such as app.example.com.';
			return;
		}

		adding = true;
		try {
			await projectsApi.addDomain(slug, projectId, environmentId, hostname.trim());
			hostname = '';
			toast.success('Added. Create the record shown, then check it.');
			await load();
		} catch (error) {
			addError = error instanceof ApiError ? error.message : 'Something went wrong.';
		} finally {
			adding = false;
		}
	}

	async function verify(domainId: string) {
		verifying = domainId;
		try {
			await projectsApi.verifyDomain(slug, projectId, domainId);
			toast.success('Pointed here. A certificate is obtained on the first visit.');
			await load();
		} catch (error) {
			toast.error(error instanceof ApiError ? error.message : 'Something went wrong.');
		} finally {
			verifying = undefined;
		}
	}

	async function remove(domainId: string) {
		try {
			await projectsApi.removeDomain(slug, projectId, domainId);
			await load();
		} catch (error) {
			toast.error(error instanceof ApiError ? error.message : 'Something went wrong.');
		}
	}

	async function copy(text: string) {
		await navigator.clipboard.writeText(text);
		toast.success('Copied.');
	}

	$effect(() => {
		void load();
	});
</script>

{#if !address}
	<div class="py-4"><Spinner /></div>
{:else}
	<div class="flex flex-col gap-4">
		<!-- How it is reached today. An address is plain HTTP: nobody issues certificates for one. -->
		{#if address.url}
			<div class="flex items-center justify-between gap-3 border border-line px-4 py-3">
				<div class="flex flex-col gap-1">
					<span class="flex items-center gap-2 numeric text-sm">
						<Icon icon={LinkSquare02Icon} size={14} class="text-ink-subtle" />
						<a href={address.url} class="hover:underline" target="_blank" rel="noreferrer">
							{address.url}
						</a>
					</span>
					{#if address.addressOnly}
						<span class="text-xs text-ink-muted">
							Reached by your server's address, over plain HTTP. Add a hostname for HTTPS.
						</span>
					{/if}
				</div>
				<button
					type="button"
					onclick={() => copy(address!.url)}
					class="text-ink-subtle hover:text-ink"
					aria-label="Copy the address"
				>
					<Icon icon={Copy01Icon} size={14} />
				</button>
			</div>
		{/if}

		{#each address.domains as domain (domain.id)}
			<div class="flex flex-col gap-3 border border-line px-4 py-3">
				<div class="flex items-center justify-between gap-3">
					<span class="flex items-center gap-2 numeric text-sm">
						<Icon
							icon={domain.verified || domain.ours ? LockKeyIcon : GlobalIcon}
							size={14}
							class="text-ink-subtle"
						/>
						{#if domain.verified || domain.ours}
							<a
								href={`https://${domain.hostname}`}
								class="hover:underline"
								target="_blank"
								rel="noreferrer">{domain.hostname}</a
							>
						{:else}
							{domain.hostname}
						{/if}
					</span>

					<span class="flex items-center gap-3">
						{#if domain.verified || domain.ours}
							<Badge tone="success">
								{domain.ours ? 'Ours' : 'Pointed here'}
							</Badge>
						{:else}
							<Badge tone="warning">Not pointed here yet</Badge>
							{#if project.permissions.manage}
								<Button
									size="sm"
									variant="secondary"
									loading={verifying === domain.id}
									onclick={() => verify(domain.id)}
								>
									Check now
								</Button>
							{/if}
						{/if}
						{#if project.permissions.manage}
							<button
								type="button"
								onclick={() => remove(domain.id)}
								class="text-ink-subtle hover:text-danger"
								aria-label={`Remove ${domain.hostname}`}
							>
								<Icon icon={Delete02Icon} size={14} />
							</button>
						{/if}
					</span>
				</div>

				<!-- Spelled out rather than described, so nobody has to work out what to create. -->
				{#if domain.record}
					<div class="flex flex-col gap-2 border-t border-line pt-3">
						<span class="text-xs text-ink-muted">
							Create this record with whoever manages {domain.hostname}, then check it.
						</span>
						<div class="grid gap-2 numeric text-xs sm:grid-cols-3">
							<span><span class="text-ink-subtle">Type</span> {domain.record.type}</span>
							<span class="truncate"
								><span class="text-ink-subtle">Name</span> {domain.record.name}</span
							>
							<span class="flex items-center gap-2">
								<span class="text-ink-subtle">Value</span>
								{domain.record.value}
								<button
									type="button"
									onclick={() => copy(domain.record!.value)}
									class="text-ink-subtle hover:text-ink"
									aria-label="Copy the value"
								>
									<Icon icon={Copy01Icon} size={12} />
								</button>
							</span>
						</div>
					</div>
				{:else if domain.verifiedAt}
					<span class="flex items-center gap-2 border-t border-line pt-3 text-xs text-ink-subtle">
						<Icon icon={CheckmarkCircle01Icon} size={13} />
						Pointed here {formatRelative(domain.verifiedAt)}. HTTPS is handled for you.
					</span>
				{/if}
			</div>
		{/each}

		{#if project.permissions.manage}
			<form onsubmit={add} class="flex flex-col gap-3 sm:flex-row sm:items-end">
				<div class="flex-1">
					<Field label="Hostname" id={`${environmentId}-hostname`} error={addError}>
						{#snippet children({ id, describedBy, invalid })}
							<Input
								{id}
								{describedBy}
								{invalid}
								bind:value={hostname}
								placeholder="app.example.com"
								mono
							/>
						{/snippet}
					</Field>
				</div>
				<Button type="submit" variant="secondary" loading={adding}>Add hostname</Button>
			</form>
			<p class="flex items-center gap-2 text-xs text-ink-subtle">
				<Icon icon={LockKeyIcon} size={13} />
				A hostname is served once it points here, and its certificate is obtained and renewed for you.
			</p>
		{/if}
	</div>
{/if}

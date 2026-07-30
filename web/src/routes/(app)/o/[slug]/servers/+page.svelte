<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { ApiError } from '$lib/api/client';
	import { organizationsApi } from '$lib/api/organizations';
	import { serversApi } from '$lib/api/servers';
	import {
		SERVER_STATUS_LABELS,
		type Organization,
		type Server,
		type ServerMode
	} from '$lib/api/types';
	import { formatBytes, formatRelative } from '$lib/format';
	import {
		Alert,
		Badge,
		Button,
		Card,
		EmptyState,
		Field,
		Icon,
		Input,
		Select,
		Spinner
	} from '$lib/ui';
	import { ArrowRight01Icon, PlusSignIcon, ServerStack01Icon } from '@hugeicons/core-free-icons';

	let slug = $derived(page.params.slug!);

	let organization = $state<Organization | null>(null);
	let servers = $state<Server[] | null>(null);
	let loadError = $state<string | undefined>();
	let connecting = $state(false);

	// Form state. Kept here rather than in a component so the whole request is visible at once.
	let name = $state('');
	let host = $state('');
	let sshPort = $state('22');
	let sshUser = $state('root');
	let mode = $state<ServerMode>('managed');
	let credential = $state<'key' | 'password'>('key');
	let secret = $state('');
	let submitting = $state(false);
	let formError = $state<string | undefined>();
	let fieldErrors = $state<Record<string, string>>({});

	async function load() {
		try {
			[organization, servers] = await Promise.all([
				organizationsApi.get(slug),
				serversApi.list(slug)
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

		// Only presence is checked here; the address is settled by trying it, and that failure
		// is reported far more usefully than a guess at the format.
		const local: Record<string, string> = {};
		if (!name.trim()) local.name = 'Give this server a name you will recognise.';
		if (!host.trim()) local.host = 'Enter the address of your server.';
		if (!secret.trim()) {
			local.secret =
				credential === 'key' ? 'Paste the private key.' : 'Enter the password for this server.';
		}
		if (Object.keys(local).length > 0) {
			fieldErrors = local;
			return;
		}

		submitting = true;
		try {
			const created = await serversApi.connect(slug, {
				name: name.trim(),
				host: host.trim(),
				sshPort: Number(sshPort) || 22,
				sshUser: sshUser.trim() || 'root',
				mode,
				...(credential === 'key' ? { key: secret } : { password: secret })
			});
			await goto(`/o/${slug}/servers/${created.id}`);
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

	function statusTone(status: Server['status']) {
		if (status === 'online') return 'success';
		if (status === 'failed' || status === 'offline') return 'danger';
		if (status === 'awaiting_choice') return 'warning';
		return 'neutral';
	}

	$effect(() => {
		void load();
	});
</script>

<svelte:head><title>Servers · yol</title></svelte:head>

<div class="flex flex-col gap-6">
	<header class="flex items-end justify-between gap-4">
		<div class="flex flex-col gap-1">
			<a href={`/o/${slug}`} class="text-xs text-ink-subtle hover:text-ink">
				← {organization?.name ?? 'Organization'}
			</a>
			<h1 class="text-xl font-semibold tracking-tight">Servers</h1>
			<p class="text-sm text-ink-muted">
				Connect a server you already own. We look at it first and change nothing until you say so.
			</p>
		</div>
		{#if servers?.length && organization?.permissions.manageServers}
			<Button size="sm" onclick={() => (connecting = true)}>
				<Icon icon={PlusSignIcon} size={14} />
				Connect a server
			</Button>
		{/if}
	</header>

	{#if loadError}
		<Alert tone="danger" title={loadError} />
	{/if}

	{#if organization && !organization.permissions.manageServers && servers?.length === 0}
		<Card>
			<EmptyState
				icon={ServerStack01Icon}
				title="No servers yet"
				description="Ask an owner or admin of this organization to connect one."
			/>
		</Card>
	{:else if connecting || servers?.length === 0}
		<Card
			title="Connect a server"
			description="We connect over SSH, look at what is already there, and report back before changing anything."
		>
			{#if formError}
				<div class="mb-4"><Alert tone="danger" title={formError} /></div>
			{/if}

			<form class="flex flex-col gap-4" onsubmit={submit}>
				<div class="grid gap-4 sm:grid-cols-2">
					<Field label="Name" id="srv-name" error={fieldErrors.name}>
						{#snippet children({ id, describedBy, invalid })}
							<Input
								{id}
								{describedBy}
								{invalid}
								bind:value={name}
								placeholder="Production server"
							/>
						{/snippet}
					</Field>

					<Field
						label="Address"
						id="srv-host"
						error={fieldErrors.host}
						hint="An IP address or hostname."
					>
						{#snippet children({ id, describedBy, invalid })}
							<Input
								{id}
								{describedBy}
								{invalid}
								bind:value={host}
								placeholder="203.0.113.9"
								mono
							/>
						{/snippet}
					</Field>

					<Field label="SSH user" id="srv-user" error={fieldErrors.sshUser}>
						{#snippet children({ id, describedBy, invalid })}
							<Input {id} {describedBy} {invalid} bind:value={sshUser} mono />
						{/snippet}
					</Field>

					<Field label="SSH port" id="srv-port" error={fieldErrors.sshPort}>
						{#snippet children({ id, describedBy, invalid })}
							<Input {id} {describedBy} {invalid} bind:value={sshPort} mono inputmode="numeric" />
						{/snippet}
					</Field>
				</div>

				<Field
					label="How should we use this server?"
					id="srv-mode"
					hint={mode === 'watch'
						? 'We will report what is on it and change nothing at all.'
						: 'We will install our agent so you can deploy to it.'}
				>
					{#snippet children({ id, describedBy, invalid })}
						<Select
							{id}
							{describedBy}
							{invalid}
							bind:value={mode}
							options={[
								{ value: 'managed', label: 'Manage it — deploy apps here' },
								{ value: 'watch', label: 'Watch only — change nothing' }
							]}
						/>
					{/snippet}
				</Field>

				<Field label="How should we sign in?" id="srv-cred">
					{#snippet children({ id, describedBy, invalid })}
						<Select
							{id}
							{describedBy}
							{invalid}
							bind:value={credential}
							options={[
								{ value: 'key', label: 'Private key' },
								{ value: 'password', label: 'Password' }
							]}
						/>
					{/snippet}
				</Field>

				<Field
					label={credential === 'key' ? 'Private key' : 'Password'}
					id="srv-secret"
					error={fieldErrors.secret ?? fieldErrors.key ?? fieldErrors.password}
					hint={credential === 'password'
						? 'Used once to set up, then deleted. We do not keep server passwords.'
						: 'Paste the whole key, including its first and last lines.'}
				>
					{#snippet children({ id, describedBy, invalid })}
						{#if credential === 'key'}
							<textarea
								{id}
								aria-describedby={describedBy}
								aria-invalid={invalid || undefined}
								bind:value={secret}
								rows="5"
								spellcheck="false"
								class={[
									'w-full border bg-surface px-3 py-2 numeric text-xs text-ink',
									'placeholder:text-ink-subtle focus:ring-1 focus:outline-none',
									invalid
										? 'border-danger focus:border-danger focus:ring-danger'
										: 'border-line-strong focus:border-ink focus:ring-ink'
								]}
								placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"></textarea>
						{:else}
							<Input {id} {describedBy} {invalid} bind:value={secret} type="password" />
						{/if}
					{/snippet}
				</Field>

				<div class="flex items-center gap-2">
					<Button type="submit" size="sm" loading={submitting}>Connect and look</Button>
					{#if servers?.length}
						<Button
							type="button"
							size="sm"
							variant="secondary"
							onclick={() => (connecting = false)}
						>
							Cancel
						</Button>
					{/if}
				</div>
			</form>
		</Card>
	{/if}

	{#if servers === null}
		<div class="flex justify-center py-14 text-ink-subtle"><Spinner size={20} /></div>
	{:else if servers.length > 0}
		<ul class="flex flex-col">
			{#each servers as server (server.id)}
				<li>
					<a
						href={`/o/${slug}/servers/${server.id}`}
						class="flex items-center justify-between gap-4 border border-b-0 border-line bg-surface px-5 py-4 last:border-b hover:bg-surface-sunken"
					>
						<div class="flex flex-col gap-1">
							<span class="flex items-center gap-2 text-sm font-medium text-ink">
								{server.name}
								{#if server.mode === 'watch'}
									<Badge>watch only</Badge>
								{/if}
							</span>
							<span class="numeric text-xs text-ink-subtle">
								{server.sshUser}@{server.host}:{server.sshPort}
							</span>
						</div>

						<div class="flex items-center gap-4">
							{#if server.facts.cpuCount && server.facts.memoryBytes}
								<span class="hidden numeric text-xs text-ink-muted sm:inline">
									{server.facts.cpuCount} CPU · {formatBytes(server.facts.memoryBytes)}
								</span>
							{/if}
							{#if server.agentLastSeenAt}
								<span class="hidden text-xs text-ink-subtle md:inline">
									seen {formatRelative(server.agentLastSeenAt)}
								</span>
							{/if}
							<Badge tone={statusTone(server.status)}>{SERVER_STATUS_LABELS[server.status]}</Badge>
							<Icon icon={ArrowRight01Icon} size={16} class="text-ink-subtle" />
						</div>
					</a>
				</li>
			{/each}
		</ul>
	{/if}
</div>

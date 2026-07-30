<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { ApiError } from '$lib/api/client';
	import { serversApi } from '$lib/api/servers';
	import {
		SERVER_STATUS_LABELS,
		type ResourceKind,
		type RoutingMode,
		type Server,
		type ServerEvent,
		type ServerResource
	} from '$lib/api/types';
	import { formatBytes, formatDateTime, formatRelative } from '$lib/format';
	import {
		Alert,
		Badge,
		Button,
		Card,
		EmptyState,
		Icon,
		Spinner,
		Table,
		TableRow,
		toast
	} from '$lib/ui';
	import { Database01Icon, ServerStack01Icon } from '@hugeicons/core-free-icons';

	let slug = $derived(page.params.slug!);
	let serverId = $derived(page.params.id!);

	let server = $state<Server | null>(null);
	let events = $state<ServerEvent[]>([]);
	let resources = $state<ServerResource[]>([]);
	let loading = $state(true);
	let loadError = $state<string | undefined>();
	let choosing = $state(false);

	// Statuses where something is still happening, so the page keeps looking for progress.
	const busy: Server['status'][] = ['pending', 'surveying', 'installing'];

	async function load() {
		try {
			server = await serversApi.get(slug, serverId);
			[events, resources] = await Promise.all([
				serversApi.events(slug, serverId),
				serversApi.resources(slug, serverId)
			]);
		} catch (error) {
			loadError =
				error instanceof ApiError ? error.message : 'Something went wrong. Please try again.';
		} finally {
			loading = false;
		}
	}

	async function choose(routingMode: RoutingMode) {
		choosing = true;
		try {
			server = await serversApi.chooseRouting(slug, serverId, routingMode);
			await load();
			toast.success('Saved. We will use this when setting up.');
		} catch (error) {
			toast.error(error instanceof ApiError ? error.message : 'Something went wrong.');
		} finally {
			choosing = false;
		}
	}

	async function disconnect() {
		try {
			await serversApi.remove(slug, serverId);
			toast.success('Server disconnected. Nothing on it was changed.');
			await goto(`/o/${slug}/servers`);
		} catch (error) {
			toast.error(error instanceof ApiError ? error.message : 'Something went wrong.');
		}
	}

	// While work is in progress the page follows along, then stops once it settles.
	$effect(() => {
		void load();
	});

	$effect(() => {
		if (!server || !busy.includes(server.status)) return;
		const timer = setInterval(load, 2000);
		return () => clearInterval(timer);
	});

	function byKind(kind: ResourceKind) {
		return resources.filter((r) => r.kind === kind);
	}

	let ours = $derived(resources.filter((r) => r.managed || r.adoptedAt));
	let theirs = $derived(byKind('container').filter((r) => !r.managed && !r.adoptedAt));
	let databases = $derived(byKind('database'));
	let ports = $derived(byKind('port').sort((a, b) => (a.ports[0] ?? 0) - (b.ports[0] ?? 0)));
	let activeServices = $derived(byKind('service').filter((r) => r.status === 'active'));

	function statusTone(status: Server['status']) {
		if (status === 'online') return 'success';
		if (status === 'failed' || status === 'offline') return 'danger';
		if (status === 'awaiting_choice') return 'warning';
		return 'neutral';
	}

	function levelTone(level: ServerEvent['level']) {
		if (level === 'error') return 'text-danger';
		if (level === 'warning') return 'text-warning';
		return 'text-ink-muted';
	}
</script>

<svelte:head><title>{server?.name ?? 'Server'} · yol</title></svelte:head>

{#if loading && !server}
	<div class="flex justify-center py-20 text-ink-subtle"><Spinner size={20} /></div>
{:else if loadError}
	<Alert tone="danger" title={loadError} />
{:else if server}
	<div class="flex flex-col gap-6">
		<header class="flex items-start justify-between gap-4">
			<div class="flex flex-col gap-1">
				<a href={`/o/${slug}/servers`} class="text-xs text-ink-subtle hover:text-ink">← Servers</a>
				<h1 class="flex items-center gap-2 text-xl font-semibold tracking-tight">
					{server.name}
					{#if server.mode === 'watch'}<Badge>watch only</Badge>{/if}
				</h1>
				<p class="numeric text-xs text-ink-subtle">
					{server.sshUser}@{server.host}:{server.sshPort}
				</p>
			</div>
			<div class="flex items-center gap-2">
				<Badge tone={statusTone(server.status)}>{SERVER_STATUS_LABELS[server.status]}</Badge>
				{#if server.permissions.delete}
					<Button size="sm" variant="ghost" onclick={disconnect}>Disconnect</Button>
				{/if}
			</div>
		</header>

		{#if server.failureReason}
			<Alert tone="danger" title={server.failureReason} />
		{/if}

		{#if server.mode === 'watch'}
			<Alert tone="neutral" title="This server is being watched only">
				We report what is on it and change nothing.
			</Alert>
		{/if}

		<!-- Once answered, the choice is shown rather than asked again. -->
		{#if server.routingMode && server.status === 'awaiting_choice'}
			<Card title="Ready to set up">
				<div class="flex items-start justify-between gap-4">
					<p class="text-sm text-ink-muted">
						{server.routingMode === 'takeover'
							? 'We will handle ports 80 and 443 on this server, including certificates.'
							: 'Your web server keeps ports 80 and 443. We will serve your apps on ports we choose, and show you what to add to its configuration.'}
					</p>
					{#if server.permissions.manage}
						<Button
							size="sm"
							variant="secondary"
							onclick={() =>
								choose(server!.routingMode === 'takeover' ? 'behind_proxy' : 'takeover')}
						>
							Change
						</Button>
					{/if}
				</div>
			</Card>
		{:else if server.status === 'awaiting_choice' && server.permissions.manage}
			<Card
				title="How should web traffic reach your apps?"
				description="Ports 80 and 443 are already in use on this server."
			>
				<div class="flex flex-col gap-3">
					<button
						type="button"
						disabled={choosing}
						onclick={() => choose('behind_proxy')}
						class="flex flex-col gap-1 border border-line px-4 py-3 text-left hover:bg-surface-sunken disabled:opacity-50"
					>
						<span class="text-sm font-medium text-ink">Keep your web server in front</span>
						<span class="text-xs text-ink-muted">
							We serve your apps on ports we choose, and you point your existing web server at them.
							Your current sites and certificates keep working exactly as they do now.
						</span>
					</button>

					<button
						type="button"
						disabled={choosing}
						onclick={() => choose('takeover')}
						class="flex flex-col gap-1 border border-line px-4 py-3 text-left hover:bg-surface-sunken disabled:opacity-50"
					>
						<span class="text-sm font-medium text-ink">Let us handle web traffic</span>
						<span class="text-xs text-ink-muted">
							We take over ports 80 and 443 and manage certificates for you. Whatever is serving
							those ports now will stop, so move those sites here first.
						</span>
					</button>
				</div>
			</Card>
		{/if}

		<div class="grid gap-6 lg:grid-cols-3">
			<div class="flex flex-col gap-6 lg:col-span-2">
				<!-- What we run. Separate from what was already here, so ownership is never ambiguous. -->
				<Card title="Managed by yol" description="Only these are ours to start, stop or remove.">
					{#if ours.length === 0}
						<EmptyState
							icon={ServerStack01Icon}
							title="Nothing here yet"
							description="Anything we run on this server will appear here."
						/>
					{:else}
						<Table columns={['Name', 'Image', 'Ports', 'Status']} caption="Managed resources">
							{#each ours as item (item.id)}
								<TableRow>
									<td class="px-4 py-3 font-medium text-ink">{item.name}</td>
									<td class="px-4 py-3 numeric text-xs text-ink-muted">{item.image ?? '—'}</td>
									<td class="px-4 py-3 numeric text-xs">{item.ports.join(', ') || '—'}</td>
									<td class="px-4 py-3 text-xs text-ink-muted">{item.status ?? '—'}</td>
								</TableRow>
							{/each}
						</Table>
					{/if}
				</Card>

				<Card
					title="Already on this server"
					description="Found here when we connected. We never change any of it."
				>
					{#if theirs.length === 0}
						<EmptyState title="Nothing else is running here" />
					{:else}
						<Table columns={['Name', 'Image', 'Ports', 'Status']} caption="Existing containers">
							{#each theirs as item (item.id)}
								<TableRow>
									<td class="px-4 py-3 font-medium text-ink">{item.name}</td>
									<td class="px-4 py-3 numeric text-xs text-ink-muted">{item.image ?? '—'}</td>
									<td class="px-4 py-3 numeric text-xs">{item.ports.join(', ') || '—'}</td>
									<td class="px-4 py-3 text-xs text-ink-muted">{item.status ?? '—'}</td>
								</TableRow>
							{/each}
						</Table>
					{/if}
				</Card>

				{#if databases.length > 0}
					<Card
						title="Databases we noticed"
						description="Recognising these is a guess, so check before relying on it. We do not touch them."
					>
						<Table columns={['Kind', 'Version', 'Port', 'Where']} caption="Detected databases">
							{#each databases as item (item.id)}
								<TableRow>
									<td class="px-4 py-3">
										<span class="flex items-center gap-2 font-medium text-ink">
											<Icon icon={Database01Icon} size={14} class="text-ink-subtle" />
											{item.name}
										</span>
									</td>
									<td class="px-4 py-3 numeric text-xs">{item.version ?? '—'}</td>
									<td class="px-4 py-3 numeric text-xs">{item.ports.join(', ') || '—'}</td>
									<td class="px-4 py-3 text-xs text-ink-muted"
										>{item.externalId.split(':')[1] ?? '—'}</td
									>
								</TableRow>
							{/each}
						</Table>
					</Card>
				{/if}

				<Card title="What happened" description="Every step, newest last.">
					{#if events.length === 0}
						<EmptyState title="Nothing recorded yet" />
					{:else}
						<ol class="flex flex-col gap-2.5">
							{#each events as event (event.id)}
								<li class="flex gap-3 text-sm">
									<time
										class="shrink-0 numeric text-xs text-ink-subtle"
										datetime={event.createdAt}
										title={formatDateTime(event.createdAt)}
									>
										{formatRelative(event.createdAt)}
									</time>
									<span class={levelTone(event.level)}>{event.message}</span>
								</li>
							{/each}
						</ol>
					{/if}
				</Card>
			</div>

			<div class="flex flex-col gap-6">
				<Card title="This machine">
					<dl class="flex flex-col gap-2.5 text-sm">
						{#each [['System', server.facts.osName ? `${server.facts.osName} ${server.facts.osVersion ?? ''}` : null], ['Architecture', server.facts.arch], ['Kernel', server.facts.kernel], ['Docker', server.facts.dockerVersion]] as [label, value] (label)}
							<div class="flex justify-between gap-4">
								<dt class="text-ink-subtle">{label}</dt>
								<dd class="text-right numeric text-xs text-ink">{value || 'Not known yet'}</dd>
							</div>
						{/each}
						<div class="flex justify-between gap-4">
							<dt class="text-ink-subtle">Processors</dt>
							<dd class="numeric text-xs text-ink">{server.facts.cpuCount ?? '—'}</dd>
						</div>
						<div class="flex justify-between gap-4">
							<dt class="text-ink-subtle">Memory</dt>
							<dd class="numeric text-xs text-ink">
								{server.facts.memoryBytes ? formatBytes(server.facts.memoryBytes) : '—'}
							</dd>
						</div>
					</dl>
				</Card>

				{#if ports.length > 0}
					<Card title="Listening" description="What is using each port.">
						<ul class="flex flex-col gap-1.5 text-sm">
							{#each ports as item (item.id)}
								<li class="flex items-baseline justify-between gap-3">
									<span class="numeric text-ink">{item.ports[0]}</span>
									<span class="truncate text-xs text-ink-muted"
										>{item.externalId.split('/')[0]}</span
									>
								</li>
							{/each}
						</ul>
					</Card>
				{/if}

				{#if activeServices.length > 0}
					<Card
						title="Running services"
						description={`${activeServices.length} active on this server.`}
					>
						<ul class="max-h-64 space-y-1 overflow-y-auto text-xs text-ink-muted">
							{#each activeServices as item (item.id)}
								<li class="truncate numeric leading-5">{item.name}</li>
							{/each}
						</ul>
					</Card>
				{/if}
			</div>
		</div>
	</div>
{/if}

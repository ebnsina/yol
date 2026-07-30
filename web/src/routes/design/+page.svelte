<script lang="ts">
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
		Table,
		TableRow,
		Toaster,
		toast
	} from '$lib/ui';
	import { formatBytes, formatDuration, formatRelative, formatPercent } from '$lib/format';
	import {
		Notification01Icon,
		ServerStack01Icon,
		PlusSignIcon,
		Logout01Icon
	} from '@hugeicons/core-free-icons';

	let email = $state('');
	let broken = $state('not-an-email');
	let role = $state('member');

	const now = Date.now();

	const people = [
		{ name: 'Owner Person', email: 'owner@example.com', role: 'owner', days: 40 },
		{ name: 'Admin Person', email: 'admin@example.com', role: 'admin', days: 12 },
		{ name: 'Member Person', email: 'member@example.com', role: 'member', days: 3 }
	];
</script>

<Toaster />

<div class="mx-auto flex max-w-3xl flex-col gap-8 px-6 py-14">
	<header class="flex flex-col gap-1">
		<h1 class="text-2xl font-semibold tracking-tight">Design foundation</h1>
		<p class="text-sm text-ink-muted">
			Mona Sans for text, Geist Mono for numbers. Panels are sharp, controls are pills.
		</p>
	</header>

	<Card title="Buttons" description="Primary is a black pill; secondary is outlined.">
		<div class="flex flex-wrap items-center gap-3">
			<Button>Deploy</Button>
			<Button variant="secondary">Cancel</Button>
			<Button variant="ghost">Skip</Button>
			<Button variant="danger">Remove</Button>
			<Button loading>Deploying</Button>
			<Button size="sm">Small</Button>
			<Button disabled>Disabled</Button>
		</div>
	</Card>

	<Card title="Icons and numbers" description="Fixed-width digits keep columns steady.">
		<div class="flex flex-wrap items-center gap-6">
			<span class="flex items-center gap-2 text-sm">
				<Icon icon={Notification01Icon} size={18} />
				Notification01Icon
			</span>
			<span class="flex items-center gap-2 text-sm">
				<Icon icon={ServerStack01Icon} size={18} />
				ServerStack01Icon
			</span>
			<span class="flex items-center gap-2 text-sm">
				<Icon icon={Logout01Icon} size={18} />
				Logout01Icon
			</span>
		</div>
		<dl class="mt-5 grid grid-cols-2 gap-x-8 gap-y-2 text-sm sm:grid-cols-4">
			<div>
				<dt class="text-xs text-ink-subtle">Memory</dt>
				<dd class="numeric">{formatBytes(1_610_612_736)}</dd>
			</div>
			<div>
				<dt class="text-xs text-ink-subtle">Build</dt>
				<dd class="numeric">{formatDuration(94_300)}</dd>
			</div>
			<div>
				<dt class="text-xs text-ink-subtle">CPU</dt>
				<dd class="numeric">{formatPercent(0.42)}</dd>
			</div>
			<div>
				<dt class="text-xs text-ink-subtle">Deployed</dt>
				<dd>{formatRelative(now - 1000 * 60 * 8)}</dd>
			</div>
		</dl>
	</Card>

	<Card title="Form fields" description="Errors sit under the field they belong to.">
		<div class="flex max-w-sm flex-col gap-4">
			<Field label="Email address" id="demo-email" hint="Where invitations are sent.">
				{#snippet children({ id, describedBy, invalid })}
					<Input
						{id}
						{describedBy}
						{invalid}
						bind:value={email}
						type="email"
						placeholder="you@example.com"
					/>
				{/snippet}
			</Field>

			<Field label="Email address" id="demo-broken" error="Please enter a valid email address.">
				{#snippet children({ id, describedBy, invalid })}
					<Input {id} {describedBy} {invalid} bind:value={broken} />
				{/snippet}
			</Field>

			<Field label="Role" id="demo-role" optional>
				{#snippet children({ id, describedBy, invalid })}
					<Select
						{id}
						{describedBy}
						{invalid}
						bind:value={role}
						options={[
							{ value: 'owner', label: 'Owner' },
							{ value: 'admin', label: 'Admin' },
							{ value: 'member', label: 'Member' },
							{ value: 'viewer', label: 'Viewer' }
						]}
					/>
				{/snippet}
			</Field>
		</div>
	</Card>

	<Card title="Messages" description="Colour appears only as status.">
		<div class="flex flex-col gap-3">
			<Alert tone="danger" title="That server stopped responding">
				We last heard from it 4 minutes ago.
			</Alert>
			<Alert tone="success" title="Deployed successfully" />
			<Alert tone="warning" title="Disk is nearly full">Only 8% of the disk remains free.</Alert>
			<Alert tone="neutral" title="A build is already running" />
		</div>
		<div class="mt-4 flex gap-2">
			<Button size="sm" variant="secondary" onclick={() => toast.success('Deployed successfully.')}>
				Show confirmation
			</Button>
			<Button
				size="sm"
				variant="secondary"
				onclick={() => toast.error('We could not reach that server.')}
			>
				Show failure
			</Button>
		</div>
	</Card>

	<Card title="Members" description="Four roles, shown as badges.">
		<Table columns={['Person', 'Role', 'Joined']} caption="Organization members">
			{#each people as person (person.email)}
				<TableRow>
					<td class="px-4 py-3">
						<div class="flex flex-col">
							<span class="font-medium">{person.name}</span>
							<span class="text-xs text-ink-subtle">{person.email}</span>
						</div>
					</td>
					<td class="px-4 py-3">
						<Badge tone={person.role === 'owner' ? 'strong' : 'neutral'}>{person.role}</Badge>
					</td>
					<td class="px-4 py-3 text-ink-muted">
						{formatRelative(now - person.days * 24 * 60 * 60 * 1000)}
					</td>
				</TableRow>
			{/each}
		</Table>
	</Card>

	<Card title="Nothing here yet">
		<EmptyState
			icon={ServerStack01Icon}
			title="No servers connected"
			description="Connect a server you already own and it will appear here."
		>
			{#snippet action()}
				<Button size="sm">
					<Icon icon={PlusSignIcon} size={14} />
					Connect a server
				</Button>
			{/snippet}
		</EmptyState>
	</Card>
</div>

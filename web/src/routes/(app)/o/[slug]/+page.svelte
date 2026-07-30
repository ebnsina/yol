<script lang="ts">
	import { page } from '$app/state';
	import { ApiError } from '$lib/api/client';
	import { organizationsApi } from '$lib/api/organizations';
	import { InviteSchema } from '$lib/api/schemas';
	import {
		ROLE_DESCRIPTIONS,
		ROLE_LABELS,
		type Invitation,
		type Member,
		type Organization,
		type Role
	} from '$lib/api/types';
	import { createForm } from '$lib/form.svelte';
	import { formatRelative } from '$lib/format';
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
		Spinner,
		Table,
		TableRow,
		toast
	} from '$lib/ui';
	import {
		Copy01Icon,
		MailAtSign01Icon,
		ServerStack01Icon,
		UserGroupIcon
	} from '@hugeicons/core-free-icons';

	let slug = $derived(page.params.slug!);

	let organization = $state<Organization | null>(null);
	let members = $state<Member[]>([]);
	let invitations = $state<Invitation[]>([]);
	let loading = $state(true);
	let loadError = $state<string | undefined>();

	let inviteEmail = $state('');
	let inviteRole = $state<Role>('member');
	let lastInviteUrl = $state<string | undefined>();

	const roleOptions = (Object.keys(ROLE_LABELS) as Role[]).map((role) => ({
		value: role,
		label: ROLE_LABELS[role]
	}));

	const inviteForm = createForm({
		schema: InviteSchema,
		submit: (values) => organizationsApi.invite(slug, { email: values.email, role: values.role }),
		onSuccess: async (result) => {
			lastInviteUrl = result.inviteUrl;
			inviteEmail = '';
			toast.success('Invitation created.');
			await refresh();
		}
	});

	async function load() {
		loading = true;
		loadError = undefined;
		try {
			organization = await organizationsApi.get(slug);
			await refresh();
		} catch (error) {
			loadError =
				error instanceof ApiError ? error.message : 'Something went wrong. Please try again.';
		} finally {
			loading = false;
		}
	}

	/** Invitations are only readable by people who can manage members, so it is asked for
	 *  only when the API says that is allowed. */
	async function refresh() {
		members = await organizationsApi.members(slug);
		invitations = organization?.permissions.manageMembers
			? await organizationsApi.invitations(slug)
			: [];
	}

	async function changeRole(member: Member, role: Role) {
		try {
			await organizationsApi.changeRole(slug, member.userId, role);
			toast.success(`${member.name} is now ${ROLE_LABELS[role].toLowerCase()}.`);
			await load();
		} catch (error) {
			toast.error(error instanceof ApiError ? error.message : 'Something went wrong.');
		}
	}

	async function removeMember(member: Member) {
		try {
			await organizationsApi.removeMember(slug, member.userId);
			toast.success(`${member.name} was removed.`);
			await load();
		} catch (error) {
			toast.error(error instanceof ApiError ? error.message : 'Something went wrong.');
		}
	}

	async function revoke(invitation: Invitation) {
		try {
			await organizationsApi.revokeInvitation(slug, invitation.id);
			toast.success('Invitation cancelled.');
			await refresh();
		} catch (error) {
			toast.error(error instanceof ApiError ? error.message : 'Something went wrong.');
		}
	}

	async function copy(url: string) {
		await navigator.clipboard.writeText(url);
		toast.show('Invitation link copied.');
	}

	$effect(() => {
		void load();
	});
</script>

<svelte:head><title>{organization?.name ?? 'Organization'} · yol</title></svelte:head>

{#if loading && !organization}
	<div class="flex justify-center py-20 text-ink-subtle"><Spinner size={20} /></div>
{:else if loadError}
	<Alert tone="danger" title={loadError} />
{:else if organization}
	<div class="flex flex-col gap-6">
		<header class="flex items-end justify-between gap-4">
			<div class="flex flex-col gap-1">
				<a href="/organizations" class="text-xs text-ink-subtle hover:text-ink">
					← All organizations
				</a>
				<h1 class="text-xl font-semibold tracking-tight">{organization.name}</h1>
			</div>
			<div class="flex items-center gap-2">
				<Button href={`/o/${slug}/servers`} size="sm" variant="secondary">
					<Icon icon={ServerStack01Icon} size={14} />
					Servers
				</Button>
				<Badge tone={organization.role === 'owner' ? 'strong' : 'neutral'}
					>{organization.role}</Badge
				>
			</div>
		</header>

		{#if lastInviteUrl}
			<Alert tone="success" title="Invitation ready to share">
				<div class="mt-1.5 flex items-center gap-2">
					<code class="flex-1 truncate border border-line bg-surface px-2 py-1 text-xs text-ink">
						{lastInviteUrl}
					</code>
					<Button size="sm" variant="secondary" onclick={() => copy(lastInviteUrl!)}>
						<Icon icon={Copy01Icon} size={14} />
						Copy
					</Button>
				</div>
			</Alert>
		{/if}

		<Card
			title="People"
			description={organization.permissions.manageMembers
				? 'Invite people and choose what they can do.'
				: 'Everyone with access to this organization.'}
		>
			<Table columns={['Person', 'Role', 'Joined', '']} caption="Members">
				{#each members as member (member.userId)}
					<TableRow>
						<td class="px-4 py-3">
							<div class="flex flex-col">
								<span class="font-medium text-ink">{member.name}</span>
								<span class="text-xs text-ink-subtle">{member.email}</span>
							</div>
						</td>
						<td class="px-4 py-3">
							{#if organization.permissions.manageMembers}
								<div class="w-32">
									<Select
										value={member.role}
										options={roleOptions}
										aria-label={`Role for ${member.name}`}
										onchange={(event) =>
											changeRole(member, (event.currentTarget as HTMLSelectElement).value as Role)}
									/>
								</div>
							{:else}
								<Badge tone={member.role === 'owner' ? 'strong' : 'neutral'}>{member.role}</Badge>
							{/if}
						</td>
						<td class="px-4 py-3 text-ink-muted">{formatRelative(member.joinedAt)}</td>
						<td class="px-4 py-3 text-right">
							<!-- The API decides whether removal is possible, including the last owner rule. -->
							{#if member.canBeRemoved}
								<Button size="sm" variant="ghost" onclick={() => removeMember(member)}
									>Remove</Button
								>
							{/if}
						</td>
					</TableRow>
				{/each}
			</Table>
		</Card>

		{#if organization.permissions.manageMembers}
			<Card title="Invite someone" description={ROLE_DESCRIPTIONS[inviteRole]}>
				{#if inviteForm.formError}
					<div class="mb-4"><Alert tone="danger" title={inviteForm.formError} /></div>
				{/if}

				<form
					class="flex flex-col gap-4 sm:flex-row sm:items-start"
					onsubmit={(event) => {
						event.preventDefault();
						inviteForm.handleSubmit({ email: inviteEmail, role: inviteRole });
					}}
				>
					<div class="flex-1">
						<Field label="Email address" id="invite-email" error={inviteForm.fieldErrors.email}>
							{#snippet children({ id, describedBy, invalid })}
								<Input
									{id}
									{describedBy}
									{invalid}
									bind:value={inviteEmail}
									type="email"
									placeholder="colleague@example.com"
									oninput={() => inviteForm.clearField('email')}
								/>
							{/snippet}
						</Field>
					</div>
					<div class="sm:w-40">
						<Field label="Role" id="invite-role" error={inviteForm.fieldErrors.role}>
							{#snippet children({ id, describedBy, invalid })}
								<Select
									{id}
									{describedBy}
									{invalid}
									bind:value={inviteRole}
									options={roleOptions}
								/>
							{/snippet}
						</Field>
					</div>
					<Button type="submit" loading={inviteForm.submitting} class="sm:mt-6"
						>Send invitation</Button
					>
				</form>
			</Card>

			<Card title="Pending invitations">
				{#if invitations.length === 0}
					<EmptyState
						icon={MailAtSign01Icon}
						title="No pending invitations"
						description="Anyone you invite will appear here until they accept."
					/>
				{:else}
					<Table columns={['Email address', 'Role', 'Expires', '']} caption="Pending invitations">
						{#each invitations as invitation (invitation.id)}
							<TableRow>
								<td class="px-4 py-3 text-ink">{invitation.email}</td>
								<td class="px-4 py-3">
									<Badge>{invitation.role}</Badge>
								</td>
								<td class="px-4 py-3 text-ink-muted">{formatRelative(invitation.expiresAt)}</td>
								<td class="px-4 py-3 text-right">
									<Button size="sm" variant="ghost" onclick={() => revoke(invitation)}
										>Cancel</Button
									>
								</td>
							</TableRow>
						{/each}
					</Table>
				{/if}
			</Card>
		{:else}
			<Card>
				<EmptyState
					icon={UserGroupIcon}
					title="Only owners and admins can invite people"
					description="Ask an owner of this organization if you need to add someone."
				/>
			</Card>
		{/if}
	</div>
{/if}

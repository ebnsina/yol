<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { ApiError } from '$lib/api/client';
	import { organizationsApi } from '$lib/api/organizations';
	import { ROLE_DESCRIPTIONS, ROLE_LABELS, type InvitationPreview } from '$lib/api/types';
	import { session } from '$lib/session.svelte';
	import { Alert, Button, Card, Spinner } from '$lib/ui';

	let token = $derived(page.params.token!);

	let invitation = $state<InvitationPreview | null>(null);
	let loading = $state(true);
	let loadError = $state<string | undefined>();
	let accepting = $state(false);
	let acceptError = $state<string | undefined>();

	// Readable while signed out, so someone can see who invited them before committing.
	$effect(() => {
		void (async () => {
			try {
				if (!session.loaded) await session.load();
				invitation = await organizationsApi.previewInvitation(token);
			} catch (error) {
				loadError =
					error instanceof ApiError ? error.message : 'Something went wrong. Please try again.';
			} finally {
				loading = false;
			}
		})();
	});

	async function accept() {
		accepting = true;
		acceptError = undefined;
		try {
			const joined = await organizationsApi.acceptInvitation(token);
			await goto(`/o/${joined.slug}`, { replaceState: true });
		} catch (error) {
			acceptError =
				error instanceof ApiError ? error.message : 'Something went wrong. Please try again.';
		} finally {
			accepting = false;
		}
	}

	let signInHref = $derived(`/login?next=${encodeURIComponent(`/invitations/${token}`)}`);
	let signUpHref = $derived(`/signup?next=${encodeURIComponent(`/invitations/${token}`)}`);
</script>

<svelte:head><title>Invitation · yol</title></svelte:head>

<div class="flex min-h-screen flex-col items-center justify-center gap-8 px-6 py-12">
	<a href="/" class="numeric text-2xs tracking-widest text-ink-subtle uppercase">yol</a>

	<div class="w-full max-w-md">
		{#if loading}
			<div class="flex justify-center py-14 text-ink-subtle"><Spinner size={20} /></div>
		{:else if loadError}
			<Card title="This invitation is no longer valid">
				<p class="text-sm text-ink-muted">{loadError}</p>
				{#snippet footer()}
					<Button href="/" variant="secondary" size="sm">Go to the dashboard</Button>
				{/snippet}
			</Card>
		{:else if invitation}
			<Card title={`Join ${invitation.organizationName}`}>
				<div class="flex flex-col gap-4">
					<dl class="flex flex-col gap-2 text-sm">
						<div class="flex justify-between gap-4">
							<dt class="text-ink-subtle">Invited address</dt>
							<dd class="text-ink">{invitation.email}</dd>
						</div>
						<div class="flex justify-between gap-4">
							<dt class="text-ink-subtle">Role</dt>
							<dd class="text-ink">{ROLE_LABELS[invitation.role]}</dd>
						</div>
					</dl>

					<p class="border-t border-line pt-3 text-xs text-ink-muted">
						{ROLE_DESCRIPTIONS[invitation.role]}
					</p>

					{#if acceptError}
						<Alert tone="danger" title={acceptError} />
					{/if}

					{#if invitation.needsSignIn}
						<p class="text-sm text-ink-muted">
							Sign in as {invitation.email} to accept this invitation.
						</p>
						<div class="flex items-center gap-2">
							<Button href={signInHref} size="sm">Sign in</Button>
							<Button href={signUpHref} size="sm" variant="secondary">Create an account</Button>
						</div>
					{:else if !invitation.matchesCurrentAccount}
						<!-- Signed in as somebody else: the API will refuse, so say so first. -->
						<Alert tone="warning" title="This invitation is for a different account">
							You are signed in as {session.user?.email}. Sign in as {invitation.email} to accept it.
						</Alert>
						<Button
							size="sm"
							variant="secondary"
							onclick={() => session.signOut(`/invitations/${token}`)}
						>
							Sign out and switch accounts
						</Button>
					{:else}
						<Button size="sm" loading={accepting} onclick={accept}>
							Join {invitation.organizationName}
						</Button>
					{/if}
				</div>
			</Card>
		{/if}
	</div>
</div>

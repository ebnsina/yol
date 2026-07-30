<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { authApi } from '$lib/api/auth';
	import { LoginSchema } from '$lib/api/schemas';
	import { createForm } from '$lib/form.svelte';
	import { session } from '$lib/session.svelte';
	import { Alert, Button, Field, Input } from '$lib/ui';

	let email = $state('');
	let password = $state('');

	// Where to land after signing in, so an invitation link is not lost on the way.
	let next = $derived(page.url.searchParams.get('next') ?? '/organizations');

	const form = createForm({
		schema: LoginSchema,
		submit: (values) => authApi.login(values),
		onSuccess: async ({ user }) => {
			session.set(user);
			await goto(next, { replaceState: true });
		}
	});
</script>

<svelte:head><title>Sign in · yol</title></svelte:head>

<div class="flex flex-col gap-6">
	<header class="flex flex-col gap-1">
		<h1 class="text-lg font-semibold tracking-tight">Sign in</h1>
		<p class="text-sm text-ink-muted">Manage the servers you already own.</p>
	</header>

	{#if form.formError}
		<Alert tone="danger" title={form.formError} />
	{/if}

	<form
		class="flex flex-col gap-4"
		onsubmit={(event) => {
			event.preventDefault();
			form.handleSubmit({ email, password });
		}}
	>
		<Field label="Email address" id="email" error={form.fieldErrors.email}>
			{#snippet children({ id, describedBy, invalid })}
				<Input
					{id}
					{describedBy}
					{invalid}
					bind:value={email}
					type="email"
					autocomplete="email"
					placeholder="you@example.com"
					oninput={() => form.clearField('email')}
				/>
			{/snippet}
		</Field>

		<Field label="Password" id="password" error={form.fieldErrors.password}>
			{#snippet children({ id, describedBy, invalid })}
				<Input
					{id}
					{describedBy}
					{invalid}
					bind:value={password}
					type="password"
					autocomplete="current-password"
					oninput={() => form.clearField('password')}
				/>
			{/snippet}
		</Field>

		<Button type="submit" loading={form.submitting} class="mt-1 w-full">Sign in</Button>
	</form>

	<p class="text-center text-sm text-ink-muted">
		No account yet?
		<a href={`/signup?next=${encodeURIComponent(next)}`} class="font-medium text-ink underline">
			Create one
		</a>
	</p>
</div>

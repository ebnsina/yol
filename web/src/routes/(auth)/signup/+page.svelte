<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { authApi } from '$lib/api/auth';
	import { SignupSchema } from '$lib/api/schemas';
	import { createForm } from '$lib/form.svelte';
	import { session } from '$lib/session.svelte';
	import { Alert, Button, Field, Input } from '$lib/ui';

	let name = $state('');
	let email = $state('');
	let password = $state('');

	let next = $derived(page.url.searchParams.get('next') ?? '/organizations');

	const form = createForm({
		schema: SignupSchema,
		submit: (values) => authApi.signup(values),
		onSuccess: async ({ user }) => {
			session.set(user);
			await goto(next, { replaceState: true });
		}
	});
</script>

<svelte:head><title>Create an account · yol</title></svelte:head>

<div class="flex flex-col gap-6">
	<header class="flex flex-col gap-1">
		<h1 class="text-lg font-semibold tracking-tight">Create an account</h1>
		<p class="text-sm text-ink-muted">Bring your own server and deploy to it.</p>
	</header>

	{#if form.formError}
		<Alert tone="danger" title={form.formError} />
	{/if}

	<form
		class="flex flex-col gap-4"
		onsubmit={(event) => {
			event.preventDefault();
			form.handleSubmit({ name, email, password });
		}}
	>
		<Field label="Your name" id="name" error={form.fieldErrors.name}>
			{#snippet children({ id, describedBy, invalid })}
				<Input
					{id}
					{describedBy}
					{invalid}
					bind:value={name}
					autocomplete="name"
					oninput={() => form.clearField('name')}
				/>
			{/snippet}
		</Field>

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

		<Field
			label="Password"
			id="password"
			error={form.fieldErrors.password}
			hint="At least 12 characters. A short phrase works well."
		>
			{#snippet children({ id, describedBy, invalid })}
				<Input
					{id}
					{describedBy}
					{invalid}
					bind:value={password}
					type="password"
					autocomplete="new-password"
					oninput={() => form.clearField('password')}
				/>
			{/snippet}
		</Field>

		<Button type="submit" loading={form.submitting} class="mt-1 w-full">Create account</Button>
	</form>

	<p class="text-center text-sm text-ink-muted">
		Already have an account?
		<a href={`/login?next=${encodeURIComponent(next)}`} class="font-medium text-ink underline">
			Sign in
		</a>
	</p>
</div>

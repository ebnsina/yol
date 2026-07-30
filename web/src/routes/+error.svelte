<script lang="ts">
	import { page } from '$app/state';
	import { Button } from '$lib/ui';

	// Plain language only. A status number means nothing to the person reading it.
	const messages: Record<number, { title: string; detail: string }> = {
		404: {
			title: 'We could not find that page',
			detail: 'The link may be out of date, or the page may have been deleted.'
		},
		403: {
			title: 'You do not have access to that',
			detail: 'Ask an owner of the organization for access.'
		},
		500: {
			title: 'Something went wrong on our end',
			detail: 'We have been notified. Please try again in a moment.'
		}
	};

	let content = $derived(
		messages[page.status] ?? {
			title: 'Something went wrong',
			detail: 'Please try again in a moment.'
		}
	);
</script>

<div class="flex min-h-screen items-center justify-center px-6">
	<div class="flex max-w-md flex-col items-center gap-5 text-center">
		<p class="numeric text-2xs tracking-widest text-ink-subtle uppercase">yol</p>

		<div class="flex flex-col gap-2">
			<h1 class="text-xl font-semibold tracking-tight text-ink">{content.title}</h1>
			<p class="text-sm leading-relaxed text-ink-muted">{content.detail}</p>
		</div>

		<div class="mt-1 flex items-center gap-2">
			<Button href="/">Go to the dashboard</Button>
			<Button variant="secondary" onclick={() => history.back()}>Go back</Button>
		</div>
	</div>
</div>

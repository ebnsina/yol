<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		label: string;
		id: string;
		error?: string;
		hint?: string;
		optional?: boolean;
		children: Snippet<[{ id: string; describedBy: string | undefined; invalid: boolean }]>;
	}

	let { label, id, error, hint, optional = false, children }: Props = $props();

	let errorId = $derived(`${id}-error`);
	let hintId = $derived(`${id}-hint`);
	// The error wins as the description, so a screen reader hears the problem, not the hint.
	let describedBy = $derived(error ? errorId : hint ? hintId : undefined);
</script>

<div class="flex flex-col gap-1.5">
	<label for={id} class="flex items-baseline justify-between text-sm font-medium text-ink">
		<span>{label}</span>
		{#if optional}
			<span class="text-xs font-normal text-ink-subtle">Optional</span>
		{/if}
	</label>

	{@render children({ id, describedBy, invalid: Boolean(error) })}

	{#if error}
		<p id={errorId} class="text-xs text-danger">{error}</p>
	{:else if hint}
		<p id={hintId} class="text-xs text-ink-subtle">{hint}</p>
	{/if}
</div>

<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		title?: string;
		description?: string;
		footer?: Snippet;
		actions?: Snippet;
		class?: string;
		children: Snippet;
	}

	// Sharp corners, hairline border, no shadow. Panels never round.
	let { title, description, footer, actions, class: className = '', children }: Props = $props();
</script>

<section class={`border border-line bg-surface ${className}`}>
	{#if title || actions}
		<header class="flex items-start justify-between gap-4 border-b border-line px-5 py-4">
			<div class="flex flex-col gap-1">
				{#if title}
					<h2 class="text-sm font-semibold text-ink">{title}</h2>
				{/if}
				{#if description}
					<p class="text-xs text-ink-muted">{description}</p>
				{/if}
			</div>
			{#if actions}
				<div class="flex shrink-0 items-center gap-2">{@render actions()}</div>
			{/if}
		</header>
	{/if}

	<div class="px-5 py-4">{@render children()}</div>

	{#if footer}
		<footer
			class="flex items-center justify-end gap-2 border-t border-line bg-surface-sunken px-5 py-3"
		>
			{@render footer()}
		</footer>
	{/if}
</section>

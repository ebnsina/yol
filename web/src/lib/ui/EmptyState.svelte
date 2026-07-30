<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { IconSvgElement } from '@hugeicons/svelte';
	import Icon from './Icon.svelte';

	interface Props {
		icon?: IconSvgElement;
		title: string;
		description?: string;
		action?: Snippet;
	}

	let { icon, title, description, action }: Props = $props();

	// A bare line of text floating in the height of a full empty state reads as something broken
	// rather than as nothing to show, so it takes only the room it needs.
	let roomy = $derived(Boolean(icon || description || action));
</script>

<div
	class={['flex flex-col items-center gap-3 px-6 text-center', roomy ? 'py-14' : 'py-6'].join(' ')}
>
	{#if icon}
		<span class="border border-line bg-surface-sunken p-2.5 text-ink-subtle">
			<Icon {icon} size={20} />
		</span>
	{/if}
	<div class="flex flex-col gap-1">
		<p class="text-sm font-medium text-ink">{title}</p>
		{#if description}
			<p class="max-w-sm text-xs leading-relaxed text-ink-muted">{description}</p>
		{/if}
	</div>
	{#if action}
		<div class="mt-1">{@render action()}</div>
	{/if}
</div>

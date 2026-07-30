<script lang="ts">
	import type { IconSvgElement } from '@hugeicons/svelte';
	import Icon from './Icon.svelte';

	export interface Tab {
		value: string;
		label: string;
		icon?: IconSvgElement;
		/** Shown beside the label, for "how many are in here" without opening it. */
		count?: number;
	}

	interface Props {
		tabs: Tab[];
		value: string;
		label: string;
	}

	// A row of sections rather than one long page. Sharp, underlined, monochrome: the active one is
	// marked by a solid rule under it, which needs no colour to read as chosen.
	let { tabs, value = $bindable(), label }: Props = $props();
</script>

<div
	class="-mb-px flex items-end gap-1 overflow-x-auto border-b border-line"
	role="tablist"
	aria-label={label}
>
	{#each tabs as tab (tab.value)}
		<button
			type="button"
			role="tab"
			aria-selected={value === tab.value}
			onclick={() => (value = tab.value)}
			class={[
				'flex shrink-0 items-center gap-2 border-b-2 px-3 py-2.5 text-sm transition-colors',
				value === tab.value
					? 'border-ink font-medium text-ink'
					: 'border-transparent text-ink-muted hover:text-ink'
			].join(' ')}
		>
			{#if tab.icon}
				<Icon icon={tab.icon} size={14} />
			{/if}
			{tab.label}
			{#if tab.count !== undefined}
				<span class="numeric text-xs text-ink-subtle">{tab.count}</span>
			{/if}
		</button>
	{/each}
</div>

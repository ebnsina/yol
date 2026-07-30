<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		columns: string[];
		caption?: string;
		children: Snippet;
	}

	// Wide tables scroll inside their own container so the page never scrolls sideways.
	let { columns, caption, children }: Props = $props();
</script>

<div class="overflow-x-auto">
	<table class="w-full border-collapse text-sm">
		{#if caption}
			<caption class="sr-only">{caption}</caption>
		{/if}
		<thead>
			<tr class="border-b border-line">
				{#each columns as column (column)}
					<th
						scope="col"
						class="px-4 py-2.5 text-left text-2xs font-medium tracking-wide text-ink-subtle uppercase"
					>
						{column}
					</th>
				{/each}
			</tr>
		</thead>
		<tbody>{@render children()}</tbody>
	</table>
</div>

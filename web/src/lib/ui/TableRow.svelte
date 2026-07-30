<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		children: Snippet;
	}

	let { children }: Props = $props();
</script>

<tr class="border-b border-line last:border-0 hover:bg-surface-sunken">
	{@render children()}
</tr>

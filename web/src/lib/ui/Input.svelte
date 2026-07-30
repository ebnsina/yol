<script lang="ts">
	import type { HTMLInputAttributes } from 'svelte/elements';

	interface Props extends Omit<HTMLInputAttributes, 'class' | 'value'> {
		value?: string;
		invalid?: boolean;
		describedBy?: string;
		mono?: boolean;
		class?: string;
	}

	let {
		value = $bindable(''),
		invalid = false,
		describedBy,
		mono = false,
		class: className = '',
		...rest
	}: Props = $props();
</script>

<input
	bind:value
	class={[
		'h-10 w-full border bg-surface px-3 text-sm text-ink transition-colors',
		'placeholder:text-ink-subtle focus:ring-1 focus:outline-none',
		invalid
			? 'border-danger focus:border-danger focus:ring-danger'
			: 'border-line-strong focus:border-ink focus:ring-ink',
		mono && 'numeric',
		className
	]}
	aria-invalid={invalid || undefined}
	aria-describedby={describedBy}
	{...rest}
/>

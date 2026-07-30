<script lang="ts">
	import type { HTMLSelectAttributes } from 'svelte/elements';

	interface Option {
		value: string;
		label: string;
	}

	interface Props extends Omit<HTMLSelectAttributes, 'class' | 'value'> {
		value?: string;
		options: Option[];
		invalid?: boolean;
		describedBy?: string;
		class?: string;
	}

	let {
		value = $bindable(''),
		options,
		invalid = false,
		describedBy,
		class: className = '',
		...rest
	}: Props = $props();
</script>

<select
	bind:value
	class={[
		'h-10 w-full appearance-none border bg-surface px-3 text-sm text-ink transition-colors',
		'focus:outline-none focus:ring-1',
		invalid
			? 'border-danger focus:border-danger focus:ring-danger'
			: 'border-line-strong focus:border-ink focus:ring-ink',
		className
	]}
	aria-invalid={invalid || undefined}
	aria-describedby={describedBy}
	{...rest}
>
	{#each options as option (option.value)}
		<option value={option.value}>{option.label}</option>
	{/each}
</select>

<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { HTMLButtonAttributes } from 'svelte/elements';

	type Variant = 'primary' | 'secondary' | 'ghost' | 'danger';
	type Size = 'sm' | 'md';

	interface Props extends Omit<HTMLButtonAttributes, 'class'> {
		variant?: Variant;
		size?: Size;
		loading?: boolean;
		href?: string;
		class?: string;
		children: Snippet;
	}

	let {
		variant = 'primary',
		size = 'md',
		loading = false,
		href,
		class: className = '',
		children,
		disabled,
		type = 'button',
		...rest
	}: Props = $props();

	// Pills, per the design language. Panels stay sharp; controls are round.
	const base =
		'inline-flex items-center justify-center gap-2 rounded-full font-medium ' +
		'whitespace-nowrap transition-colors disabled:pointer-events-none disabled:opacity-40';

	const variants: Record<Variant, string> = {
		primary: 'bg-ink text-ink-inverse hover:bg-ink-muted',
		secondary: 'border border-line-strong text-ink hover:bg-surface-sunken',
		ghost: 'text-ink-muted hover:bg-surface-sunken hover:text-ink',
		danger: 'bg-danger text-ink-inverse hover:opacity-90'
	};

	const sizes: Record<Size, string> = {
		sm: 'h-8 px-3.5 text-xs',
		md: 'h-10 px-5 text-sm'
	};

	let inactive = $derived(disabled || loading);
	let classes = $derived(`${base} ${variants[variant]} ${sizes[size]} ${className}`);
</script>

{#if href}
	<a {href} class={classes} aria-disabled={inactive} {...rest as object}>
		{@render children()}
	</a>
{:else}
	<button {type} class={classes} disabled={inactive} aria-busy={loading} {...rest}>
		{#if loading}
			<span
				class="size-3 animate-spin rounded-full border border-current border-t-transparent"
				aria-hidden="true"
			></span>
		{/if}
		{@render children()}
	</button>
{/if}

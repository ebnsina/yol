<script lang="ts">
	import type { Snippet } from 'svelte';

	type Tone = 'neutral' | 'strong' | 'danger' | 'success' | 'warning';

	interface Props {
		tone?: Tone;
		mono?: boolean;
		children: Snippet;
	}

	let { tone = 'neutral', mono = false, children }: Props = $props();

	const tones: Record<Tone, string> = {
		neutral: 'border-line bg-surface-sunken text-ink-muted',
		strong: 'border-ink bg-ink text-ink-inverse',
		danger: 'border-danger/30 bg-danger-surface text-danger',
		success: 'border-success/30 bg-success-surface text-success',
		warning: 'border-warning/30 bg-warning-surface text-warning'
	};
</script>

<span
	class={[
		'inline-flex items-center border px-2 py-0.5 text-2xs font-medium tracking-wide uppercase',
		tones[tone],
		mono && 'numeric'
	]}
>
	{@render children()}
</span>

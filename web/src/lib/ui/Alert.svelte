<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { IconSvgElement } from '@hugeicons/svelte';
	import Icon from './Icon.svelte';
	import {
		Alert01Icon,
		CheckmarkCircle01Icon,
		InformationCircleIcon
	} from '@hugeicons/core-free-icons';

	type Tone = 'danger' | 'success' | 'warning' | 'neutral';

	interface Props {
		tone?: Tone;
		title?: string;
		children?: Snippet;
	}

	let { tone = 'neutral', title, children }: Props = $props();

	const tones: Record<Tone, { box: string; icon: IconSvgElement }> = {
		danger: { box: 'border-danger/30 bg-danger-surface text-danger', icon: Alert01Icon },
		success: { box: 'border-success/30 bg-success-surface text-success', icon: CheckmarkCircle01Icon },
		warning: { box: 'border-warning/30 bg-warning-surface text-warning', icon: Alert01Icon },
		neutral: { box: 'border-line bg-surface-sunken text-ink', icon: InformationCircleIcon }
	};

	let current = $derived(tones[tone]);
	// Failures are announced immediately; the rest wait for a pause in speech.
	let live = $derived<'assertive' | 'polite'>(tone === 'danger' ? 'assertive' : 'polite');
</script>

<div
	class={`flex items-start gap-2.5 border px-3.5 py-3 ${current.box}`}
	role={tone === 'danger' ? 'alert' : 'status'}
	aria-live={live}
>
	<Icon icon={current.icon} size={16} class="mt-0.5" />
	<div class="flex flex-col gap-0.5 text-sm">
		{#if title}
			<p class="font-medium">{title}</p>
		{/if}
		{#if children}
			<div class="text-ink-muted">{@render children()}</div>
		{/if}
	</div>
</div>

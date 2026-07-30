<script lang="ts">
	import Icon from './Icon.svelte';
	import { Cancel01Icon } from '@hugeicons/core-free-icons';
	import { toast, type ToastTone } from './toast.svelte';

	const tones: Record<ToastTone, string> = {
		neutral: 'border-line bg-surface text-ink',
		success: 'border-success/30 bg-success-surface text-success',
		danger: 'border-danger/30 bg-danger-surface text-danger'
	};
</script>

<div
	class="pointer-events-none fixed inset-x-0 bottom-0 z-50 flex flex-col items-center gap-2 p-4"
	aria-live="polite"
>
	{#each toast.items as item (item.id)}
		<div
			class={`pointer-events-auto flex w-full max-w-sm items-start gap-2.5 border px-3.5 py-3 text-sm ${tones[item.tone]}`}
		>
			<p class="flex-1">{item.message}</p>
			<button
				type="button"
				class="shrink-0 text-current opacity-60 hover:opacity-100"
				onclick={() => toast.dismiss(item.id)}
				aria-label="Dismiss"
			>
				<Icon icon={Cancel01Icon} size={14} />
			</button>
		</div>
	{/each}
</div>

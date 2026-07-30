<script lang="ts">
	/**
	 * The only place the icon library is imported. Everything else imports this, so swapping
	 * libraries touches one file.
	 */
	import { HugeiconsIcon, type IconSvgElement } from '@hugeicons/svelte';

	interface Props {
		icon: IconSvgElement;
		size?: number;
		strokeWidth?: number;
		class?: string;
		label?: string;
	}

	let { icon, size = 16, strokeWidth = 1.5, class: className = '', label }: Props = $props();
</script>

<span
	class={`inline-flex shrink-0 items-center justify-center ${className}`}
	role={label ? 'img' : 'presentation'}
	aria-label={label}
	aria-hidden={label ? undefined : 'true'}
>
	<HugeiconsIcon {icon} {size} {strokeWidth} color="currentColor" />
</span>

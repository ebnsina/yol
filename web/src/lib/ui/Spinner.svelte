<script lang="ts">
	interface Props {
		size?: number;
		label?: string;
	}

	let { size = 16, label = 'Loading' }: Props = $props();
</script>

<span
	class="inline-block animate-spin rounded-full border border-current border-t-transparent"
	style={`width:${size}px;height:${size}px`}
	role="status"
	aria-label={label}
></span>

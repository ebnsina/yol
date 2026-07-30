<script lang="ts">
	let { children } = $props();
</script>

<div class="flex min-h-screen flex-col items-center justify-center gap-8 px-6 py-12">
	<a href="/" class="numeric text-2xs tracking-widest text-ink-subtle uppercase">yol</a>
	<div class="w-full max-w-sm">{@render children()}</div>
</div>

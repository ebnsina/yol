<script lang="ts">
	import { goto } from '$app/navigation';
	import { session } from '$lib/session.svelte';
	import { Spinner } from '$lib/ui';

	// The entry point only decides where to send someone.
	$effect(() => {
		void (async () => {
			if (!session.loaded) await session.load();
			await goto(session.signedIn ? '/organizations' : '/login', { replaceState: true });
		})();
	});
</script>

<div class="flex min-h-screen items-center justify-center text-ink-subtle">
	<Spinner size={20} label="Loading" />
</div>

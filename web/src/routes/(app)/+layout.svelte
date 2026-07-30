<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { session } from '$lib/session.svelte';
	import { Button, Icon, Spinner } from '$lib/ui';
	import { Logout01Icon, Notification01Icon } from '@hugeicons/core-free-icons';

	let { children } = $props();

	let checking = $state(true);

	// One guard for every signed-in page. It sends people back here after signing in.
	$effect(() => {
		void (async () => {
			if (!session.loaded) await session.load();
			if (!session.signedIn) {
				const next = encodeURIComponent(page.url.pathname + page.url.search);
				await goto(`/login?next=${next}`, { replaceState: true });
				return;
			}
			checking = false;
		})();
	});
</script>

{#if checking}
	<div class="flex min-h-screen items-center justify-center text-ink-subtle">
		<Spinner size={20} label="Loading" />
	</div>
{:else}
	<div class="flex min-h-screen flex-col">
		<header class="border-b border-line">
			<div class="mx-auto flex h-14 max-w-5xl items-center justify-between gap-4 px-6">
				<a href="/organizations" class="numeric text-2xs tracking-widest text-ink uppercase">
					yol
				</a>

				<div class="flex items-center gap-1">
					<Button variant="ghost" size="sm" aria-label="Notifications">
						<Icon icon={Notification01Icon} size={16} />
					</Button>
					<span class="mx-1 hidden text-xs text-ink-muted sm:inline">
						{session.user?.email}
					</span>
					<Button variant="ghost" size="sm" onclick={() => session.signOut()}>
						<Icon icon={Logout01Icon} size={16} />
						<span class="hidden sm:inline">Sign out</span>
					</Button>
				</div>
			</div>
		</header>

		<main class="mx-auto w-full max-w-5xl flex-1 px-6 py-10">
			{@render children()}
		</main>
	</div>
{/if}

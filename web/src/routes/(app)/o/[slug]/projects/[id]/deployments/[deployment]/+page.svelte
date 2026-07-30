<script lang="ts">
	import { page } from '$app/state';
	import { ApiError } from '$lib/api/client';
	import { projectsApi } from '$lib/api/projects';
	import { DEPLOYMENT_STATUS_LABELS, type Deployment, type DeploymentLine } from '$lib/api/types';
	import { formatDateTime, formatRelative, formatTime } from '$lib/format';
	import { Alert, Badge, Button, Card, Icon, Spinner, toast } from '$lib/ui';
	import { ArrowTurnBackwardIcon } from '@hugeicons/core-free-icons';

	let slug = $derived(page.params.slug!);
	let projectId = $derived(page.params.id!);
	let deploymentId = $derived(page.params.deployment!);

	let deployment = $state<Deployment | null>(null);
	let lines = $state<DeploymentLine[]>([]);
	let loadError = $state<string | undefined>();
	let rollingBack = $state(false);
	let output = $state<HTMLDivElement | null>(null);

	// While a deploy is happening there is more to come, so it is asked for again. Once it has
	// finished there is nothing further and the asking stops.
	let running = $derived(
		deployment?.status === 'queued' ||
			deployment?.status === 'building' ||
			deployment?.status === 'deploying'
	);

	async function load() {
		try {
			deployment = await projectsApi.deployment(slug, projectId, deploymentId);
		} catch (error) {
			loadError =
				error instanceof ApiError ? error.message : 'Something went wrong. Please try again.';
		}
	}

	/** Asks only for what arrived after the last line held, so a long build is not re-read. */
	async function pullLines() {
		const since = lines.length > 0 ? lines[lines.length - 1].at : undefined;
		try {
			const fresh = await projectsApi.deploymentLogs(slug, projectId, deploymentId, since);
			if (fresh.length > 0) {
				lines = [...lines, ...fresh];
				// Following the output means staying at the end of it.
				queueMicrotask(() => output?.scrollTo({ top: output.scrollHeight }));
			}
		} catch {
			// A single missed poll is not worth interrupting somebody watching a build.
		}
	}

	async function rollback() {
		rollingBack = true;
		try {
			const back = await projectsApi.rollback(slug, projectId, deploymentId);
			toast.success('Going back to this version. Nothing is being rebuilt.');
			window.location.href = `/o/${slug}/projects/${projectId}/deployments/${back.id}`;
		} catch (error) {
			toast.error(error instanceof ApiError ? error.message : 'Something went wrong.');
		} finally {
			rollingBack = false;
		}
	}

	function statusTone(status: Deployment['status']) {
		if (status === 'live') return 'success';
		if (status === 'failed') return 'danger';
		if (status === 'building' || status === 'deploying') return 'warning';
		return 'neutral';
	}

	$effect(() => {
		void load();
		void pullLines();
	});

	$effect(() => {
		if (!running) return;

		const timer = setInterval(() => {
			void load();
			void pullLines();
		}, 2000);
		return () => clearInterval(timer);
	});
</script>

<svelte:head><title>Deployment · yol</title></svelte:head>

{#if loadError}
	<Alert tone="danger" title={loadError} />
{:else if !deployment}
	<div class="flex justify-center py-16"><Spinner /></div>
{:else}
	<div class="flex flex-col gap-6">
		<header class="flex items-end justify-between gap-4">
			<div class="flex flex-col gap-1">
				<a href={`/o/${slug}/projects/${projectId}`} class="text-xs text-ink-subtle hover:text-ink">
					← Project
				</a>
				<div class="flex items-center gap-3">
					<h1 class="font-mono text-lg font-semibold tracking-tight">
						{deployment.commitSha?.slice(0, 7) ?? 'Deployment'}
					</h1>
					<Badge tone={statusTone(deployment.status)}>
						{DEPLOYMENT_STATUS_LABELS[deployment.status]}
					</Badge>
				</div>
				<p class="text-sm text-ink-muted">
					{#if deployment.commitRef}
						From <span class="font-mono">{deployment.commitRef}</span>, started
					{:else}
						Started
					{/if}
					{formatRelative(deployment.createdAt)}. Built on your own server.
				</p>
			</div>

			{#if deployment.status === 'superseded' && deployment.imageRef}
				<Button variant="secondary" onclick={rollback} loading={rollingBack}>
					<Icon icon={ArrowTurnBackwardIcon} size={14} />
					Go back to this
				</Button>
			{/if}
		</header>

		{#if deployment.failureReason}
			<Alert tone="danger" title="This deploy did not finish">
				{deployment.failureReason}
			</Alert>
		{/if}

		{#if deployment.status === 'failed'}
			<Alert tone="neutral" title="Whatever was serving before is still serving">
				Traffic only moves to a new version once it answers, so a failed deploy changes nothing.
			</Alert>
		{/if}

		<Card
			title="Output"
			description={running ? 'Following as it happens.' : 'What this deploy printed.'}
		>
			{#snippet actions()}
				{#if running}
					<Spinner />
				{/if}
			{/snippet}

			{#if lines.length === 0}
				<p class="text-sm text-ink-muted">
					{running ? 'Waiting for the server to start.' : 'This deploy printed nothing.'}
				</p>
			{:else}
				<div
					bind:this={output}
					class="max-h-[28rem] overflow-y-auto bg-surface-sunken p-3 font-mono text-xs leading-relaxed"
				>
					{#each lines as line, i (i)}
						<div class="flex gap-3 whitespace-pre-wrap">
							<span class="shrink-0 text-ink-subtle">{formatTime(line.at)}</span>
							<span class={line.stream === 'stderr' ? 'text-danger' : ''}>{line.text}</span>
						</div>
					{/each}
				</div>
			{/if}
		</Card>

		<Card title="Details">
			<dl class="grid gap-4 text-sm sm:grid-cols-2">
				<div class="flex flex-col gap-1">
					<dt class="text-xs text-ink-muted">Commit</dt>
					<dd class="font-mono text-xs">{deployment.commitSha ?? '—'}</dd>
				</div>
				<div class="flex flex-col gap-1">
					<dt class="text-xs text-ink-muted">Image</dt>
					<dd class="font-mono text-xs">{deployment.imageRef ?? 'Not built yet'}</dd>
				</div>
				<div class="flex flex-col gap-1">
					<dt class="text-xs text-ink-muted">Started</dt>
					<dd>{deployment.startedAt ? formatDateTime(deployment.startedAt) : 'Not yet'}</dd>
				</div>
				<div class="flex flex-col gap-1">
					<dt class="text-xs text-ink-muted">Finished</dt>
					<dd>{deployment.finishedAt ? formatDateTime(deployment.finishedAt) : 'Not yet'}</dd>
				</div>
			</dl>
		</Card>
	</div>
{/if}

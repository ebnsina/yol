<script lang="ts">
	import { ApiError } from '$lib/api/client';
	import { projectsApi } from '$lib/api/projects';
	import {
		DEPLOYMENT_STATUS_LABELS,
		type Deployment,
		type Environment,
		type Project,
		type Server,
		type Variable
	} from '$lib/api/types';
	import { formatBytes, formatRelative } from '$lib/format';
	import { Alert, Badge, Button, Card, Field, Icon, Input, Select, Spinner, toast } from '$lib/ui';
	import { Delete02Icon, RocketIcon } from '@hugeicons/core-free-icons';

	interface Props {
		slug: string;
		project: Project;
		environment: Environment;
		servers: Server[];
		onchanged: () => void;
	}

	let { slug, project, environment, servers, onchanged }: Props = $props();

	let app = $derived(environment.services?.find((s) => s.kind === 'app'));

	let deployments = $state<Deployment[] | null>(null);
	let variables = $state<Variable[] | null>(null);
	let deploying = $state(false);

	// Seeded from what the API says and then owned by the field, so typing is not overwritten by
	// a reload happening underneath.
	let serverId = $state('');
	let branch = $state('');
	let healthPath = $state('');
	let healthPort = $state('');
	let editing = $state(false);

	$effect(() => {
		if (editing) return;
		serverId = environment.serverId ?? '';
		branch = environment.branch;
		healthPath = app?.healthPath ?? '';
		healthPort = app?.healthPort ? String(app.healthPort) : '';
	});

	let variableName = $state('');
	let variableValue = $state('');
	let savingVariable = $state(false);
	let variableError = $state<string | undefined>();

	let savingService = $state(false);
	let showSettings = $state(false);

	// A version already serving is what a rollback goes back from, so it is worth naming.
	let live = $derived(deployments?.find((d) => d.status === 'live'));

	async function load() {
		if (!app) return;
		try {
			[deployments, variables] = await Promise.all([
				projectsApi.deployments(slug, project.id, app.id),
				projectsApi.variables(slug, project.id, environment.id)
			]);
		} catch (error) {
			// Nothing to show is better than a broken screen; the reason reaches the toast.
			toast.error(error instanceof ApiError ? error.message : 'Something went wrong.');
		}
	}

	async function changeServer() {
		try {
			await projectsApi.updateEnvironment(slug, project.id, environment.id, { serverId });
			toast.success('This environment will deploy there from now on.');
			onchanged();
		} catch (error) {
			toast.error(error instanceof ApiError ? error.message : 'Something went wrong.');
		}
	}

	async function changeBranch() {
		if (branch.trim() === environment.branch) return;
		try {
			await projectsApi.updateEnvironment(slug, project.id, environment.id, {
				branch: branch.trim()
			});
			editing = false;
			toast.success(`Pushes to ${branch.trim()} now deploy here.`);
			onchanged();
		} catch (error) {
			toast.error(error instanceof ApiError ? error.message : 'Something went wrong.');
			branch = environment.branch;
		}
	}

	async function deploy() {
		deploying = true;
		try {
			const started = await projectsApi.deploy(slug, project.id, environment.id);
			toast.success('Building. This runs on your own server.');
			window.location.href = `/o/${slug}/projects/${project.id}/deployments/${started.id}`;
		} catch (error) {
			toast.error(error instanceof ApiError ? error.message : 'Something went wrong.');
		} finally {
			deploying = false;
		}
	}

	async function saveVariable(event: SubmitEvent) {
		event.preventDefault();
		variableError = undefined;
		if (!variableName.trim()) {
			variableError = 'Give this variable a name.';
			return;
		}

		savingVariable = true;
		try {
			await projectsApi.setVariable(
				slug,
				project.id,
				environment.id,
				variableName.trim(),
				variableValue
			);
			variableName = '';
			variableValue = '';
			toast.success('Saved. Your next deploy will use it.');
			await load();
		} catch (error) {
			variableError = error instanceof ApiError ? error.message : 'Something went wrong.';
		} finally {
			savingVariable = false;
		}
	}

	async function removeVariable(name: string) {
		try {
			await projectsApi.deleteVariable(slug, project.id, environment.id, name);
			await load();
		} catch (error) {
			toast.error(error instanceof ApiError ? error.message : 'Something went wrong.');
		}
	}

	async function saveService() {
		if (!app) return;
		savingService = true;
		try {
			await projectsApi.updateService(slug, project.id, app.id, {
				healthPath: healthPath.trim(),
				healthPort: healthPort ? Number(healthPort) : undefined
			});
			editing = false;
			toast.success('Saved. Your next deploy is checked this way.');
			onchanged();
		} catch (error) {
			toast.error(error instanceof ApiError ? error.message : 'Something went wrong.');
		} finally {
			savingService = false;
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
	});
</script>

<Card title={environment.name}>
	{#snippet actions()}
		{#if project.permissions.deploy}
			<Button size="sm" onclick={deploy} loading={deploying} disabled={!environment.serverId}>
				<Icon icon={RocketIcon} size={14} />
				Deploy
			</Button>
		{/if}
	{/snippet}

	<div class="flex flex-col gap-5">
		{#if !environment.serverId}
			<Alert tone="warning" title="This environment has nowhere to run">
				Choose one of your servers before deploying.
			</Alert>
		{/if}

		<div class="grid gap-4 sm:grid-cols-2">
			<Field label="Server" id={`${environment.id}-server`} hint="Where this environment runs.">
				{#snippet children({ id })}
					<Select
						{id}
						bind:value={serverId}
						onchange={changeServer}
						disabled={!project.permissions.manage}
						options={[
							{ value: '', label: 'Not chosen yet' },
							...servers.map((server) => ({
								value: server.id,
								label: `${server.name} · ${server.host}`
							}))
						]}
					/>
				{/snippet}
			</Field>

			<Field
				label="Branch"
				id={`${environment.id}-branch`}
				hint="Pushing here deploys this environment."
			>
				{#snippet children({ id })}
					<Input
						{id}
						bind:value={branch}
						onfocus={() => (editing = true)}
						onblur={changeBranch}
						disabled={!project.permissions.manage}
						mono
					/>
				{/snippet}
			</Field>
		</div>

		<!-- Deployments -->
		<div class="flex flex-col gap-2">
			<div class="flex items-center justify-between">
				<h3 class="text-xs font-semibold tracking-wide text-ink-muted uppercase">Deployments</h3>
				{#if app}
					<span class="text-xs text-ink-subtle">
						{#if live}
							Serving {live.commitSha?.slice(0, 7) ?? 'a version'}
						{:else}
							Nothing serving yet
						{/if}
					</span>
				{/if}
			</div>

			{#if !deployments}
				<div class="py-4"><Spinner /></div>
			{:else if deployments.length === 0}
				<p class="text-sm text-ink-muted">
					Nothing deployed yet. Connect a repository and press deploy.
				</p>
			{:else}
				<div class="flex flex-col border border-line">
					{#each deployments.slice(0, 5) as deployment (deployment.id)}
						<a
							href={`/o/${slug}/projects/${project.id}/deployments/${deployment.id}`}
							class="flex items-center justify-between gap-3 px-4 py-3 text-sm not-first:border-t not-first:border-line hover:bg-surface-raised"
						>
							<span class="flex items-center gap-3">
								<Badge tone={statusTone(deployment.status)}>
									{DEPLOYMENT_STATUS_LABELS[deployment.status]}
								</Badge>
								<span class="font-mono text-xs">
									{deployment.commitSha?.slice(0, 7) ?? '—'}
								</span>
							</span>
							<span class="text-xs text-ink-subtle">{formatRelative(deployment.createdAt)}</span>
						</a>
					{/each}
				</div>
			{/if}
		</div>

		<!-- Variables. Values are write-only: the API never sends one back. -->
		<div class="flex flex-col gap-2">
			<h3 class="text-xs font-semibold tracking-wide text-ink-muted uppercase">Variables</h3>

			{#if variables?.length}
				<div class="flex flex-col border border-line">
					{#each variables as variable (variable.name)}
						<div
							class="flex items-center justify-between gap-3 px-4 py-2.5 text-sm not-first:border-t not-first:border-line"
						>
							<span class="font-mono text-xs">{variable.name}</span>
							<span class="flex items-center gap-3">
								<span class="text-xs text-ink-subtle">
									changed {formatRelative(variable.updatedAt)}
								</span>
								{#if project.permissions.deploy}
									<button
										type="button"
										onclick={() => removeVariable(variable.name)}
										class="text-ink-subtle hover:text-danger"
										aria-label={`Remove ${variable.name}`}
									>
										<Icon icon={Delete02Icon} size={14} />
									</button>
								{/if}
							</span>
						</div>
					{/each}
				</div>
			{/if}

			{#if project.permissions.deploy}
				<form onsubmit={saveVariable} class="flex flex-col gap-3 sm:flex-row sm:items-start">
					<div class="flex-1">
						<Field label="Name" id={`${environment.id}-var-name`} error={variableError}>
							{#snippet children({ id, describedBy, invalid })}
								<Input
									{id}
									{describedBy}
									{invalid}
									bind:value={variableName}
									placeholder="DATABASE_URL"
									mono
								/>
							{/snippet}
						</Field>
					</div>
					<div class="flex-1">
						<Field label="Value" id={`${environment.id}-var-value`}>
							{#snippet children({ id })}
								<Input
									{id}
									bind:value={variableValue}
									type="password"
									placeholder="Never shown again"
								/>
							{/snippet}
						</Field>
					</div>
					<Button type="submit" variant="secondary" loading={savingVariable}>Save</Button>
				</form>
				<p class="text-xs text-ink-subtle">
					Values are stored encrypted and never shown again. Your next deploy carries them.
				</p>
			{/if}
		</div>

		<!-- How this app is checked before traffic moves to it. -->
		{#if app && project.permissions.manage}
			<div class="flex flex-col gap-2">
				<button
					type="button"
					onclick={() => (showSettings = !showSettings)}
					class="self-start text-xs text-ink-subtle hover:text-ink"
				>
					{showSettings ? 'Hide' : 'Show'} health check and limits
				</button>

				{#if showSettings}
					<div class="grid gap-4 sm:grid-cols-2">
						<Field
							label="Health path"
							id={`${environment.id}-health-path`}
							hint="Asked for before traffic moves to a new version."
						>
							{#snippet children({ id })}
								<Input
									{id}
									bind:value={healthPath}
									onfocus={() => (editing = true)}
									placeholder="/healthz"
									mono
								/>
							{/snippet}
						</Field>
						<Field
							label="Port"
							id={`${environment.id}-health-port`}
							hint="Where your app listens inside its container."
						>
							{#snippet children({ id })}
								<Input
									{id}
									bind:value={healthPort}
									onfocus={() => (editing = true)}
									placeholder="3000"
									inputmode="numeric"
									mono
								/>
							{/snippet}
						</Field>
					</div>
					<div class="flex items-center gap-3">
						<Button variant="secondary" onclick={saveService} loading={savingService}>Save</Button>
						<span class="text-xs text-ink-subtle">
							Memory limit {formatBytes(app.memoryLimitBytes)}
						</span>
					</div>
				{/if}
			</div>
		{/if}
	</div>
</Card>

import { api } from './client';
import type {
	Address,
	Deployment,
	DeploymentLine,
	Environment,
	Installation,
	Project,
	Domain,
	Repository,
	Service,
	Variable
} from './types';

export interface EnvironmentChanges {
	branch?: string;
	serverId?: string;
}

export interface ServiceChanges {
	healthPath?: string;
	healthPort?: number;
	memoryLimitBytes?: number;
}

const base = (slug: string) => `/v1/organizations/${slug}/projects`;

export const projectsApi = {
	list: (slug: string) => api.get<{ projects: Project[] }>(base(slug)).then((r) => r.projects),

	create: (slug: string, name: string) =>
		api.post<{ project: Project }>(base(slug), { name }).then((r) => r.project),

	get: (slug: string, id: string) =>
		api.get<{ project: Project }>(`${base(slug)}/${id}`).then((r) => r.project),

	remove: (slug: string, id: string) => api.delete<void>(`${base(slug)}/${id}`),

	updateEnvironment: (slug: string, id: string, envId: string, changes: EnvironmentChanges) =>
		api
			.patch<{ environment: Environment }>(`${base(slug)}/${id}/environments/${envId}`, changes)
			.then((r) => r.environment),

	updateService: (slug: string, id: string, serviceId: string, changes: ServiceChanges) =>
		api
			.patch<{ service: Service }>(`${base(slug)}/${id}/services/${serviceId}`, changes)
			.then((r) => r.service),

	// Only names come back. There is no endpoint that returns a value, on purpose.
	variables: (slug: string, id: string, envId: string) =>
		api
			.get<{ variables: Variable[] }>(`${base(slug)}/${id}/environments/${envId}/variables`)
			.then((r) => r.variables),

	setVariable: (slug: string, id: string, envId: string, name: string, value: string) =>
		api.put<void>(
			`${base(slug)}/${id}/environments/${envId}/variables/${encodeURIComponent(name)}`,
			{ value }
		),

	deleteVariable: (slug: string, id: string, envId: string, name: string) =>
		api.delete<void>(
			`${base(slug)}/${id}/environments/${envId}/variables/${encodeURIComponent(name)}`
		),

	deploy: (slug: string, id: string, envId: string) =>
		api
			.post<{ deployment: Deployment }>(`${base(slug)}/${id}/environments/${envId}/deployments`, {})
			.then((r) => r.deployment),

	deployments: (slug: string, id: string, serviceId: string) =>
		api
			.get<{ deployments: Deployment[] }>(`${base(slug)}/${id}/services/${serviceId}/deployments`)
			.then((r) => r.deployments),

	deployment: (slug: string, id: string, deploymentId: string) =>
		api
			.get<{ deployment: Deployment }>(`${base(slug)}/${id}/deployments/${deploymentId}`)
			.then((r) => r.deployment),

	// `since` lets a client following a build ask only for lines it does not already hold.
	deploymentLogs: (slug: string, id: string, deploymentId: string, since?: string) =>
		api
			.get<{ lines: DeploymentLine[] }>(
				`${base(slug)}/${id}/deployments/${deploymentId}/logs${
					since ? `?since=${encodeURIComponent(since)}` : ''
				}`
			)
			.then((r) => r.lines),

	rollback: (slug: string, id: string, deploymentId: string) =>
		api
			.post<{ deployment: Deployment }>(
				`${base(slug)}/${id}/deployments/${deploymentId}/rollback`,
				{}
			)
			.then((r) => r.deployment),

	address: (slug: string, id: string, envId: string) =>
		api
			.get<{ address: Address }>(`${base(slug)}/${id}/environments/${envId}/address`)
			.then((r) => r.address),

	addDomain: (slug: string, id: string, envId: string, hostname: string) =>
		api
			.post<{ domain: Domain }>(`${base(slug)}/${id}/environments/${envId}/domains`, { hostname })
			.then((r) => r.domain),

	verifyDomain: (slug: string, id: string, domainId: string) =>
		api
			.post<{ domain: Domain }>(`${base(slug)}/${id}/domains/${domainId}/verify`, {})
			.then((r) => r.domain),

	removeDomain: (slug: string, id: string, domainId: string) =>
		api.delete<void>(`${base(slug)}/${id}/domains/${domainId}`),

	connectRepository: (
		slug: string,
		id: string,
		input: { installationId: string; repositoryId: string; fullName: string }
	) => api.put<{ project: Project }>(`${base(slug)}/${id}/repository`, input).then((r) => r.project)
};

/** Where code comes from: the address to grant access at, and what has been granted. */
export const codeApi = {
	overview: (slug: string) =>
		api.get<{ installUrl: string; installations: Installation[] }>(
			`/v1/organizations/${slug}/github`
		),

	connectInstallation: (slug: string, installationId: string) =>
		api
			.post<{ installation: Installation }>(`/v1/organizations/${slug}/github/installations`, {
				installationId
			})
			.then((r) => r.installation),

	repositories: (slug: string, installationId: string) =>
		api
			.get<{ repositories: Repository[] }>(
				`/v1/organizations/${slug}/github/installations/${installationId}/repositories`
			)
			.then((r) => r.repositories)
};

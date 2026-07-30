import { api } from './client';
import type { RoutingMode, Server, ServerEvent, ServerMode, ServerResource } from './types';

export interface ConnectServerInput {
	name: string;
	host: string;
	sshPort: number;
	sshUser: string;
	mode: ServerMode;
	key?: string;
	password?: string;
}

const base = (slug: string) => `/v1/organizations/${slug}/servers`;

export const serversApi = {
	list: (slug: string) => api.get<{ servers: Server[] }>(base(slug)).then((r) => r.servers),

	connect: (slug: string, input: ConnectServerInput) =>
		api.post<{ server: Server }>(base(slug), input).then((r) => r.server),

	get: (slug: string, id: string) =>
		api.get<{ server: Server }>(`${base(slug)}/${id}`).then((r) => r.server),

	remove: (slug: string, id: string) => api.delete<void>(`${base(slug)}/${id}`),

	chooseRouting: (slug: string, id: string, routingMode: RoutingMode) =>
		api
			.patch<{ server: Server }>(`${base(slug)}/${id}/routing`, { routingMode })
			.then((r) => r.server),

	// `since` lets a client watching a setup ask only for what is new.
	events: (slug: string, id: string, since?: string) =>
		api
			.get<{ events: ServerEvent[] }>(
				`${base(slug)}/${id}/events${since ? `?since=${encodeURIComponent(since)}` : ''}`
			)
			.then((r) => r.events),

	resources: (slug: string, id: string) =>
		api
			.get<{ resources: ServerResource[] }>(`${base(slug)}/${id}/resources`)
			.then((r) => r.resources)
};

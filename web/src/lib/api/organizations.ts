import { api } from './client';
import type { Invitation, InvitationPreview, Member, Organization, Role } from './types';

export const organizationsApi = {
	list: () =>
		api.get<{ organizations: Organization[] }>('/v1/organizations').then((r) => r.organizations),

	create: (name: string) =>
		api
			.post<{ organization: Organization }>('/v1/organizations', { name })
			.then((r) => r.organization),

	get: (slug: string) =>
		api
			.get<{ organization: Organization }>(`/v1/organizations/${slug}`)
			.then((r) => r.organization),

	rename: (slug: string, name: string) =>
		api
			.patch<{ organization: Organization }>(`/v1/organizations/${slug}`, { name })
			.then((r) => r.organization),

	members: (slug: string) =>
		api.get<{ members: Member[] }>(`/v1/organizations/${slug}/members`).then((r) => r.members),

	changeRole: (slug: string, userId: string, role: Role) =>
		api.patch<void>(`/v1/organizations/${slug}/members/${userId}`, { role }),

	removeMember: (slug: string, userId: string) =>
		api.delete<void>(`/v1/organizations/${slug}/members/${userId}`),

	invitations: (slug: string) =>
		api
			.get<{ invitations: Invitation[] }>(`/v1/organizations/${slug}/invitations`)
			.then((r) => r.invitations),

	invite: (slug: string, input: { email: string; role: Role }) =>
		api.post<{ invitation: Invitation; inviteUrl: string }>(
			`/v1/organizations/${slug}/invitations`,
			input
		),

	revokeInvitation: (slug: string, id: string) =>
		api.delete<void>(`/v1/organizations/${slug}/invitations/${id}`),

	previewInvitation: (token: string) =>
		api
			.get<{ invitation: InvitationPreview }>(`/v1/invitations/${token}`)
			.then((r) => r.invitation),

	acceptInvitation: (token: string) =>
		api
			.post<{ organization: Organization }>(`/v1/invitations/${token}/accept`)
			.then((r) => r.organization)
};

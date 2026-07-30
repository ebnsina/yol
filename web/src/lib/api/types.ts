/** Mirrors the API's response shapes. Kept together so a change is easy to trace. */

export interface User {
	id: string;
	email: string;
	name: string;
	emailVerified: boolean;
}

export type Role = 'owner' | 'admin' | 'member' | 'viewer';

/** Sent by the API for the asking caller. Clients show or hide controls from these flags
 *  rather than working the rules out from a role. */
export interface Permissions {
	manageOrganization: boolean;
	manageMembers: boolean;
	manageServers: boolean;
	deploy: boolean;
	viewLogs: boolean;
}

export interface Organization {
	id: string;
	name: string;
	slug: string;
	role: Role;
	permissions: Permissions;
	createdAt: string;
}

export interface Member {
	userId: string;
	email: string;
	name: string;
	role: Role;
	joinedAt: string;
	canBeRemoved: boolean;
}

export interface Invitation {
	id: string;
	email: string;
	role: Role;
	expiresAt: string;
	createdAt: string;
}

export interface InvitationPreview {
	organizationName: string;
	organizationSlug: string;
	email: string;
	role: Role;
	matchesCurrentAccount: boolean;
	needsSignIn: boolean;
}

export const ROLE_LABELS: Record<Role, string> = {
	owner: 'Owner',
	admin: 'Admin',
	member: 'Member',
	viewer: 'Viewer'
};

/** What each role can do, in words, so the picker explains itself. */
export const ROLE_DESCRIPTIONS: Record<Role, string> = {
	owner: 'Full control, including billing and deleting the organization.',
	admin: 'Manage servers, deployments and people.',
	member: 'Deploy and view logs.',
	viewer: 'View logs only.'
};

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

export type ServerMode = 'managed' | 'watch';

export type ServerStatus =
	'pending' | 'surveying' | 'awaiting_choice' | 'installing' | 'online' | 'offline' | 'failed';

export type RoutingMode = 'takeover' | 'behind_proxy';

export interface ServerFacts {
	osName: string | null;
	osVersion: string | null;
	arch: string | null;
	kernel: string | null;
	cpuCount: number | null;
	memoryBytes: number | null;
	dockerVersion: string | null;
}

export interface Server {
	id: string;
	name: string;
	host: string;
	sshPort: number;
	sshUser: string;
	mode: ServerMode;
	status: ServerStatus;
	routingMode: RoutingMode | null;
	facts: ServerFacts;
	permissions: { manage: boolean; delete: boolean };
	failureReason: string | null;
	agentVersion: string | null;
	agentLastSeenAt: string | null;
	createdAt: string;
}

export interface ServerEvent {
	id: string;
	step: string;
	message: string;
	level: 'info' | 'warning' | 'error';
	createdAt: string;
}

export type ResourceKind = 'container' | 'image' | 'volume' | 'service' | 'port' | 'database';

export interface ServerResource {
	id: string;
	kind: ResourceKind;
	externalId: string;
	name: string;
	status: string | null;
	image: string | null;
	version: string | null;
	ports: number[];
	sizeBytes: number | null;
	managed: boolean;
	adoptedAt: string | null;
	lastSeenAt: string;
}

/** Written for someone reading a dashboard, not for a log file. */
export const SERVER_STATUS_LABELS: Record<ServerStatus, string> = {
	pending: 'Waiting',
	surveying: 'Looking at this server',
	awaiting_choice: 'Needs a decision',
	installing: 'Setting up',
	online: 'Connected',
	offline: 'Not responding',
	failed: 'Setup did not finish'
};

/** A project holds the environments an app is deployed into. */
export interface Project {
	id: string;
	name: string;
	slug: string;
	repository: { provider: string; fullName: string } | null;
	environments?: Environment[];
	createdAt: string;
	permissions: { deploy: boolean; manage: boolean };
}

export interface Environment {
	id: string;
	name: string;
	branch: string;
	serverId: string | null;
	services?: Service[];
}

export type ServiceKind =
	'app' | 'postgres' | 'mysql' | 'redis' | 'clickhouse' | 'sqlite' | 'srs' | 'mediamtx';

export interface Service {
	id: string;
	name: string;
	kind: ServiceKind;
	healthPath: string | null;
	healthPort: number | null;
	memoryLimitBytes: number;
}

export type DeploymentStatus =
	'queued' | 'building' | 'deploying' | 'live' | 'failed' | 'superseded';

export interface Deployment {
	id: string;
	status: DeploymentStatus;
	commitSha: string | null;
	commitRef: string | null;
	imageRef: string | null;
	failureReason: string | null;
	createdAt: string;
	startedAt: string | null;
	finishedAt: string | null;
}

/** In words, because a status on its own does not say what is happening. */
export const DEPLOYMENT_STATUS_LABELS: Record<DeploymentStatus, string> = {
	queued: 'Waiting to start',
	building: 'Building',
	deploying: 'Going out',
	live: 'Live',
	failed: 'Failed',
	superseded: 'Replaced'
};

export interface DeploymentLine {
	stream: string;
	text: string;
	at: string;
}

/** Access somebody granted us on GitHub. */
export interface Installation {
	id: string;
	account: string;
	createdAt: string;
}

export interface Repository {
	id: number;
	fullName: string;
	private: boolean;
	defaultBranch: string;
}

/** A variable's name and when it changed. The value is never sent back. */
export interface Variable {
	name: string;
	updatedAt: string;
}

import { goto } from '$app/navigation';
import { authApi } from './api/auth';
import { ApiError } from './api/client';
import type { User } from './api/types';

/**
 * Who is signed in. The only client-held state, and it is a cache of what the API says
 * rather than a second source of truth. The session token lives in an httpOnly cookie that
 * scripts cannot read, so nothing sensitive is held here.
 */
let user = $state<User | null>(null);
let loaded = $state(false);

async function load() {
	try {
		const { user: current } = await authApi.me();
		user = current;
	} catch (error) {
		// Not being signed in is an ordinary outcome here, not a failure to report.
		if (error instanceof ApiError && error.needsSignIn) {
			user = null;
		} else {
			throw error;
		}
	} finally {
		loaded = true;
	}
}

/** Signing out from an invitation should come back to it, so the link is not lost. */
async function signOut(returnTo?: string) {
	await authApi.logout();
	user = null;
	const destination = returnTo ? `/login?next=${encodeURIComponent(returnTo)}` : '/login';
	await goto(destination, { replaceState: true, invalidateAll: true });
}

export const session = {
	get user() {
		return user;
	},
	get loaded() {
		return loaded;
	},
	get signedIn() {
		return user !== null;
	},
	set(current: User) {
		user = current;
		loaded = true;
	},
	load,
	signOut
};

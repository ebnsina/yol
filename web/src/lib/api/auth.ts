import { api } from './client';
import type { User } from './types';

interface SessionResponse {
	user: User;
}

export const authApi = {
	signup: (input: { name: string; email: string; password: string }) =>
		api.post<SessionResponse>('/v1/auth/signup', input),

	login: (input: { email: string; password: string }) =>
		api.post<SessionResponse>('/v1/auth/login', input),

	logout: () => api.post<void>('/v1/auth/logout'),

	me: () => api.get<SessionResponse>('/v1/auth/me')
};

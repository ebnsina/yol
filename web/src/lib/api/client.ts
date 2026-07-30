import { PUBLIC_API_URL } from '$env/static/public';

/** Stable machine codes from the API. Used to branch behaviour, never to build text. */
export type ErrorCode =
	| 'invalid_input'
	| 'not_authenticated'
	| 'not_authorized'
	| 'not_found'
	| 'already_exists'
	| 'credentials_failed'
	| 'rate_limited'
	| 'conflict'
	| 'internal'
	| 'unreachable';

/**
 * A failed request. `message` is authored by the API and is displayed as-is; this client
 * never writes user-facing copy of its own, so every surface says the same thing.
 */
export class ApiError extends Error {
	readonly code: ErrorCode;
	readonly status: number;
	readonly fields: Record<string, string>;
	readonly requestId?: string;

	constructor(init: {
		code: ErrorCode;
		status: number;
		message: string;
		fields?: Record<string, string>;
		requestId?: string;
	}) {
		super(init.message);
		this.name = 'ApiError';
		this.code = init.code;
		this.status = init.status;
		this.fields = init.fields ?? {};
		this.requestId = init.requestId;
	}

	/** True when the caller should be sent to sign in. */
	get needsSignIn() {
		return this.code === 'not_authenticated';
	}
}

type Method = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';

interface RequestOptions {
	body?: unknown;
	signal?: AbortSignal;
}

/**
 * The single path to the API. Credentials ride on the session cookie, which scripts cannot
 * read, so no token is ever held in JavaScript.
 */
async function request<T>(method: Method, path: string, options: RequestOptions = {}): Promise<T> {
	let response: Response;
	try {
		response = await fetch(`${PUBLIC_API_URL}${path}`, {
			method,
			credentials: 'include',
			headers: options.body ? { 'Content-Type': 'application/json' } : undefined,
			body: options.body ? JSON.stringify(options.body) : undefined,
			signal: options.signal
		});
	} catch (cause) {
		if (cause instanceof DOMException && cause.name === 'AbortError') throw cause;
		// The one message not authored by the API, because the API was never reached.
		throw new ApiError({
			code: 'unreachable',
			status: 0,
			message: 'We could not reach the server. Please check your connection and try again.'
		});
	}

	if (response.status === 204) return undefined as T;

	const payload = await readJson(response);

	if (!response.ok) {
		throw toApiError(response.status, payload);
	}
	return payload as T;
}

async function readJson(response: Response): Promise<unknown> {
	const text = await response.text();
	if (!text) return undefined;
	try {
		return JSON.parse(text);
	} catch {
		return undefined;
	}
}

/** Reads the API's error envelope, falling back only when the response is not one. */
function toApiError(status: number, payload: unknown): ApiError {
	const envelope = (payload as { error?: Record<string, unknown> } | undefined)?.error;

	if (envelope && typeof envelope.message === 'string') {
		return new ApiError({
			code: (envelope.code as ErrorCode) ?? 'internal',
			status,
			message: envelope.message,
			fields: (envelope.fields as Record<string, string>) ?? {},
			requestId: envelope.requestId as string | undefined
		});
	}

	return new ApiError({
		code: 'internal',
		status,
		message: 'Something went wrong. Please try again.'
	});
}

export const api = {
	get: <T>(path: string, options?: RequestOptions) => request<T>('GET', path, options),
	post: <T>(path: string, body?: unknown, options?: RequestOptions) =>
		request<T>('POST', path, { ...options, body }),
	patch: <T>(path: string, body?: unknown, options?: RequestOptions) =>
		request<T>('PATCH', path, { ...options, body }),
	put: <T>(path: string, body?: unknown, options?: RequestOptions) =>
		request<T>('PUT', path, { ...options, body }),
	delete: <T>(path: string, options?: RequestOptions) => request<T>('DELETE', path, options)
};

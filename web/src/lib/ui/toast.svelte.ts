export type ToastTone = 'neutral' | 'success' | 'danger';

export interface Toast {
	id: number;
	tone: ToastTone;
	message: string;
}

const DISMISS_AFTER_MS = 5000;

let items = $state<Toast[]>([]);
let nextId = 0;

function push(tone: ToastTone, message: string) {
	const id = nextId++;
	items = [...items, { id, tone, message }];
	setTimeout(() => dismiss(id), DISMISS_AFTER_MS);
}

function dismiss(id: number) {
	items = items.filter((item) => item.id !== id);
}

/**
 * Confirmations and failures. Messages passed in come from the API wherever one exists, so
 * this never authors copy of its own.
 */
export const toast = {
	get items() {
		return items;
	},
	show: (message: string) => push('neutral', message),
	success: (message: string) => push('success', message),
	error: (message: string) => push('danger', message),
	dismiss
};

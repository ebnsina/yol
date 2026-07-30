/**
 * Every date and number the interface shows passes through here, using the platform's Intl
 * APIs rather than hand-written formatting. Formatters are built once because constructing
 * them is the expensive part.
 */

const dateTime = new Intl.DateTimeFormat(undefined, {
	dateStyle: 'medium',
	timeStyle: 'short'
});

const dateOnly = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' });

const timeOnly = new Intl.DateTimeFormat(undefined, {
	hour: '2-digit',
	minute: '2-digit',
	second: '2-digit'
});

const relative = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });

const decimal = new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 });
const percent = new Intl.NumberFormat(undefined, { style: 'percent', maximumFractionDigits: 0 });

/** Largest units first, so a gap picks the coarsest unit that still reads naturally. */
const RELATIVE_UNITS: Array<[Intl.RelativeTimeFormatUnit, number]> = [
	['year', 365 * 24 * 60 * 60 * 1000],
	['month', 30 * 24 * 60 * 60 * 1000],
	['week', 7 * 24 * 60 * 60 * 1000],
	['day', 24 * 60 * 60 * 1000],
	['hour', 60 * 60 * 1000],
	['minute', 60 * 1000]
];

function toDate(value: Date | string | number): Date {
	return value instanceof Date ? value : new Date(value);
}

export function formatDateTime(value: Date | string | number): string {
	return dateTime.format(toDate(value));
}

export function formatDate(value: Date | string | number): string {
	return dateOnly.format(toDate(value));
}

export function formatTime(value: Date | string | number): string {
	return timeOnly.format(toDate(value));
}

/** "2 minutes ago", "in 3 days", "just now". */
export function formatRelative(value: Date | string | number, now: Date = new Date()): string {
	const elapsed = toDate(value).getTime() - now.getTime();

	for (const [unit, size] of RELATIVE_UNITS) {
		if (Math.abs(elapsed) >= size) {
			return relative.format(Math.round(elapsed / size), unit);
		}
	}
	return 'just now';
}

const BYTE_UNITS = ['B', 'kB', 'MB', 'GB', 'TB', 'PB'];

/** Decimal units, matching how disk and memory are advertised. */
export function formatBytes(bytes: number): string {
	if (!Number.isFinite(bytes) || bytes < 0) return '—';
	if (bytes < 1000) return `${bytes} B`;

	let value = bytes;
	let unit = 0;
	while (value >= 1000 && unit < BYTE_UNITS.length - 1) {
		value /= 1000;
		unit += 1;
	}
	// One decimal below 10 keeps "1.4 GB" readable without pretending to more precision.
	const digits = value < 10 ? 1 : 0;
	return `${value.toFixed(digits)} ${BYTE_UNITS[unit]}`;
}

export function formatNumber(value: number): string {
	return decimal.format(value);
}

/** Takes a fraction, so 0.42 reads as 42%. */
export function formatPercent(fraction: number): string {
	return percent.format(fraction);
}

/** Compact durations for build and deploy times: "1.4s", "2m 30s", "1h 5m". */
export function formatDuration(milliseconds: number): string {
	if (!Number.isFinite(milliseconds) || milliseconds < 0) return '—';
	if (milliseconds < 1000) return `${Math.round(milliseconds)}ms`;

	const totalSeconds = Math.round(milliseconds / 1000);
	if (totalSeconds < 60) return `${(milliseconds / 1000).toFixed(1)}s`;

	const minutes = Math.floor(totalSeconds / 60);
	const seconds = totalSeconds % 60;
	if (minutes < 60) return seconds ? `${minutes}m ${seconds}s` : `${minutes}m`;

	const hours = Math.floor(minutes / 60);
	const remainingMinutes = minutes % 60;
	return remainingMinutes ? `${hours}h ${remainingMinutes}m` : `${hours}h`;
}

/** Shortens a commit for display; the full value stays available for copying. */
export function formatCommit(sha: string): string {
	return sha.slice(0, 7);
}

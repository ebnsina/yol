import * as v from 'valibot';
import { ApiError } from './api/client';
import { validate, type FieldErrors } from './api/schemas';

/**
 * Shared submit handling for every form. It keeps local format checks and API errors in one
 * shape, so a field message displays identically whether it came from valibot or the server.
 * The API is authoritative: its errors replace local ones.
 */
export function createForm<S extends v.GenericSchema, R>(options: {
	schema: S;
	submit: (values: v.InferOutput<S>) => Promise<R>;
	onSuccess?: (result: R) => void | Promise<void>;
}) {
	let submitting = $state(false);
	let fieldErrors = $state<FieldErrors>({});
	let formError = $state<string | undefined>();

	function clear() {
		fieldErrors = {};
		formError = undefined;
	}

	/** Clears one field's message as the person types, so it stops nagging mid-correction. */
	function clearField(name: string) {
		if (fieldErrors[name]) {
			const { [name]: _removed, ...rest } = fieldErrors;
			fieldErrors = rest;
		}
	}

	async function handleSubmit(input: unknown) {
		if (submitting) return;
		clear();

		const local = validate(options.schema, input);
		if (!local.data) {
			fieldErrors = local.errors;
			return;
		}

		submitting = true;
		try {
			const result = await options.submit(local.data);
			await options.onSuccess?.(result);
		} catch (error) {
			applyError(error);
		} finally {
			submitting = false;
		}
	}

	/** Messages are shown exactly as the API wrote them; nothing is composed here. */
	function applyError(error: unknown) {
		if (error instanceof ApiError) {
			fieldErrors = error.fields;
			// Only surface the summary when no field owns the problem, to avoid saying it twice.
			formError = Object.keys(error.fields).length === 0 ? error.message : undefined;
			return;
		}
		formError = 'Something went wrong. Please try again.';
	}

	return {
		get submitting() {
			return submitting;
		},
		get fieldErrors() {
			return fieldErrors;
		},
		get formError() {
			return formError;
		},
		handleSubmit,
		clearField,
		clear
	};
}

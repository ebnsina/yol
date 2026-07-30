import * as v from 'valibot';

/**
 * Client-side schemas exist for one purpose: telling someone a field is empty or malformed
 * without a round trip. They deliberately do NOT reproduce business rules — uniqueness,
 * permissions and anything requiring stored state belong to the API, which stays
 * authoritative. Where a rule appears in both places, the wording matches the API so the
 * message does not change when validation moves from local to remote.
 */

const email = v.pipe(
	v.string(),
	v.trim(),
	v.nonEmpty('Please enter your email address.'),
	v.email('Please enter a valid email address.')
);

// Twelve characters, matching the API. Length beats character-class rules.
const password = v.pipe(
	v.string(),
	v.nonEmpty('Please choose a password.'),
	v.minLength(12, 'Please use at least 12 characters. A short phrase works well.'),
	v.maxLength(200, 'Please use fewer than 200 characters.')
);

const name = v.pipe(
	v.string(),
	v.trim(),
	v.nonEmpty('Please enter your name.'),
	v.maxLength(80, 'Please use a shorter name.')
);

export const SignupSchema = v.object({ name, email, password });

export const LoginSchema = v.object({
	email: v.pipe(v.string(), v.trim(), v.nonEmpty('Please enter your email address.')),
	password: v.pipe(v.string(), v.nonEmpty('Please enter your password.'))
});

export const OrganizationNameSchema = v.object({
	name: v.pipe(
		v.string(),
		v.trim(),
		v.nonEmpty('Please enter a name for your organization.'),
		v.maxLength(60, 'Please use a shorter name.')
	)
});

export const InviteSchema = v.object({
	email,
	role: v.picklist(['owner', 'admin', 'member', 'viewer'], 'Please choose a role.')
});

/** Field name to message, the same shape the API returns so both feed one display path. */
export type FieldErrors = Record<string, string>;

export interface ValidationResult<T> {
	data?: T;
	errors: FieldErrors;
}

/**
 * Validates input and returns the first message per field. Only the first is kept because
 * showing someone three complaints about one input at once is noise.
 */
export function validate<S extends v.GenericSchema>(
	schema: S,
	input: unknown
): ValidationResult<v.InferOutput<S>> {
	const result = v.safeParse(schema, input);
	if (result.success) {
		return { data: result.output, errors: {} };
	}

	const flat = v.flatten(result.issues);
	const errors: FieldErrors = {};
	for (const [field, messages] of Object.entries(flat.nested ?? {})) {
		if (messages?.length) errors[field] = messages[0];
	}
	return { errors };
}

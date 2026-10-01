type AcceptanceCriteriaValidation =
	| { criteria: string[]; error: null }
	| { criteria: null; error: 'count' }
	| { criteria: null; error: 'length' | 'nul'; line: number };

export function validateAcceptanceCriteria(value: string): AcceptanceCriteriaValidation {
	const criteria = value
		.split('\n')
		.map((line) => line.trim())
		.filter(Boolean);
	if (criteria.length > 50) return { criteria: null, error: 'count' };
	const encoder = new TextEncoder();
	for (const [index, criterion] of criteria.entries()) {
		if (criterion.includes('\0')) return { criteria: null, error: 'nul', line: index + 1 };
		if (encoder.encode(criterion).length > 2000) return { criteria: null, error: 'length', line: index + 1 };
	}
	return { criteria, error: null };
}

const REPOSITORY_PART = /^[A-Za-z0-9_.-]+$/;

interface ParsedRepositoryUrl {
	url: URL;
	path: string[];
}

export function githubOrigin(instanceURL: string): string | null {
	if (typeof instanceURL !== 'string' || instanceURL.trim() === '') return null;
	try {
		const url = new URL(instanceURL);
		if ((url.protocol !== 'https:' && url.protocol !== 'http:') || url.host === '') return null;
		return url.origin;
	} catch {
		return null;
	}
}

// Mirrors the backend client: github.com serves its REST API from
// api.github.com, while a GitHub Enterprise Server origin serves it under
// {origin}/api/v3.
export function githubApiBaseUrl(instanceURL: string): string | null {
	const origin = githubOrigin(instanceURL);
	if (!origin) return null;
	if (origin.toLowerCase() === 'https://github.com') return 'https://api.github.com';
	return `${origin}/api/v3`;
}

function parseRepositoryUrl(
	value: unknown,
	repositoryFullName: string,
	instanceURL: string
): ParsedRepositoryUrl | null {
	const repository = repositoryFullName.split('/');
	if (
		repository.length !== 2 ||
		repository.some((part) => !REPOSITORY_PART.test(part) || part === '.' || part === '..')
	) {
		return null;
	}

	const origin = githubOrigin(instanceURL);
	if (!origin || typeof value !== 'string' || value.trim() !== value || value === '') return null;

	let url: URL;
	try {
		url = new URL(value);
	} catch {
		return null;
	}
	if (
		url.origin.toLowerCase() !== origin.toLowerCase() ||
		url.username !== '' ||
		url.password !== '' ||
		url.search !== '' ||
		url.hash !== ''
	) {
		return null;
	}

	let path: string[];
	try {
		path = url.pathname
			.split('/')
			.slice(1)
			.map((part) => decodeURIComponent(part));
	} catch {
		return null;
	}
	if (
		path.length < 2 ||
		path[0].toLowerCase() !== repository[0].toLowerCase() ||
		path[1].toLowerCase() !== repository[1].toLowerCase()
	) {
		return null;
	}

	return { url, path };
}

export function safeGitHubRepositoryUrl(
	value: unknown,
	repositoryFullName: string,
	instanceURL: string
): string | null {
	return parseRepositoryUrl(value, repositoryFullName, instanceURL)?.url.href ?? null;
}

export function safeGitHubPullRequestUrl(
	value: unknown,
	repositoryFullName: string,
	instanceURL: string
): string | null {
	const parsed = parseRepositoryUrl(value, repositoryFullName, instanceURL);
	if (
		!parsed ||
		parsed.path.length !== 4 ||
		(parsed.path[2] !== 'pull' && parsed.path[2] !== 'pulls') ||
		!/^[1-9]\d*$/.test(parsed.path[3])
	) {
		return null;
	}
	return parsed.url.href;
}

export function safeGitHubBranchUrl(value: unknown, repositoryFullName: string, instanceURL: string): string | null {
	const parsed = parseRepositoryUrl(value, repositoryFullName, instanceURL);
	if (
		!parsed ||
		parsed.path.length < 4 ||
		(parsed.path[2] !== 'tree' && parsed.path[2] !== 'src') ||
		parsed.path.slice(3).some((part) => part === '')
	) {
		return null;
	}
	return parsed.url.href;
}

export function safeGitHubCommitUrl(value: unknown, repositoryFullName: string, instanceURL: string): string | null {
	const parsed = parseRepositoryUrl(value, repositoryFullName, instanceURL);
	if (!parsed || parsed.path.length !== 4 || parsed.path[2] !== 'commit' || !/^[0-9a-f]{7,64}$/i.test(parsed.path[3])) {
		return null;
	}
	return parsed.url.href;
}

export function githubRepositoryHomeUrl(repositoryFullName: string, instanceURL: string): string | null {
	const origin = githubOrigin(instanceURL);
	if (!origin) return null;
	return safeGitHubRepositoryUrl(`${origin}/${repositoryFullName}`, repositoryFullName, instanceURL);
}

export function safeGitHubActionsUrl(value: unknown, repositoryFullName: string, instanceURL: string): string | null {
	const parsed = parseRepositoryUrl(value, repositoryFullName, instanceURL);
	if (
		!parsed ||
		parsed.path.length !== 5 ||
		parsed.path[2] !== 'actions' ||
		parsed.path[3] !== 'runs' ||
		!/^[1-9]\d*$/.test(parsed.path[4])
	) {
		return null;
	}
	return parsed.url.href;
}

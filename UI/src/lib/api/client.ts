import { goto } from '$app/navigation';
import { authState } from '$lib/features/auth/auth.state.svelte';

class ApiClient {
	private baseUrl = '';
	private refreshing: Promise<void> | null = null;

	async fetch<T>(path: string, options: RequestInit = {}): Promise<T> {
		const res = await this.fetchResponse(path, options);

		if (!res.ok) {
			const error = await res.json().catch(() => ({
				error: { code: 'UNKNOWN', message: res.statusText }
			}));
			throw error;
		}

		if (res.status === 204) return undefined as T;
		const body = await res.text();
		if (!body) return undefined as T;
		return JSON.parse(body) as T;
	}

	private async fetchResponse(path: string, options: RequestInit = {}): Promise<Response> {
		const headers = new Headers(options.headers);
		if (options.body && !(options.body instanceof FormData) && !headers.has('Content-Type')) {
			headers.set('Content-Type', 'application/json');
		}
		const res = await fetch(`${this.baseUrl}${path}`, {
			...options,
			credentials: 'include',
			headers
		});

		if (res.status === 401) {
			if (!path.includes('/auth/refresh')) {
				await this.refresh();
				return this.fetchResponse(path, options);
			}
			// The refresh cookie is dead, so any cached user is no longer real.
			authState.clear();
			goto('/login');
			throw new Error('Unauthorized');
		}

		return res;
	}

	private async refresh(): Promise<void> {
		if (this.refreshing) return this.refreshing;
		this.refreshing = this.refreshAcrossContexts().finally(() => {
			this.refreshing = null;
		});
		return this.refreshing;
	}

	// The cookie jar is shared by every context of one browser profile (tab,
	// second tab, installed PWA), and the server rotates the refresh token on
	// every refresh. Serializing through a Web Lock keeps those contexts from
	// racing the rotation; the server's grace window covers browsers without
	// the Locks API.
	private refreshAcrossContexts(): Promise<void> {
		const locks = typeof navigator !== 'undefined' ? navigator.locks : undefined;
		if (!locks) {
			return this.fetch<void>('/api/auth/refresh', { method: 'POST' });
		}
		return locks.request<void>(
			'strifira:auth-refresh',
			{ mode: 'exclusive' },
			() => this.fetch<void>('/api/auth/refresh', { method: 'POST' })
		);
	}

	get<T>(path: string): Promise<T> {
		return this.fetch<T>(path);
	}

	post<T>(path: string, body?: unknown): Promise<T> {
		return this.fetch<T>(path, { method: 'POST', body: body ? JSON.stringify(body) : undefined });
	}

	patch<T>(path: string, body?: unknown): Promise<T> {
		return this.fetch<T>(path, { method: 'PATCH', body: body ? JSON.stringify(body) : undefined });
	}

	put<T>(path: string, body?: unknown): Promise<T> {
		return this.fetch<T>(path, { method: 'PUT', body: body ? JSON.stringify(body) : undefined });
	}

	delete<T>(path: string): Promise<T> {
		return this.fetch<T>(path, { method: 'DELETE' });
	}

	deleteWithBody<T>(path: string, body?: unknown): Promise<T> {
		return this.fetch<T>(path, { method: 'DELETE', body: body ? JSON.stringify(body) : undefined });
	}

	async blob(path: string): Promise<{ blob: Blob; filename: string | null }> {
		const res = await this.fetchResponse(path);
		if (!res.ok) throw await res.json().catch(() => ({ error: { code: 'UNKNOWN', message: res.statusText } }));
		const disposition = res.headers.get('Content-Disposition');
		const filename = disposition?.match(/filename="?([^";]+)"?/i)?.[1] ?? null;
		return { blob: await res.blob(), filename };
	}

	postForm<T>(path: string, form: FormData): Promise<T> {
		return this.fetch<T>(path, { method: 'POST', body: form });
	}
}

export const api = new ApiClient();

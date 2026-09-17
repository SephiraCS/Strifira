import { api } from './client';

export interface APIKey {
	id: string;
	name: string;
	key_prefix: string;
	scopes: string[];
	created_at: string;
	last_used_at?: string;
	expires_at?: string;
	revoked_at?: string;
}

export interface CreatedAPIKey extends APIKey {
	token: string;
}

export function listAPIKeys(): Promise<APIKey[]> {
	return api.get<APIKey[]>(`/api/api-keys`);
}

export function createAPIKey(data: {
	name: string;
	read_only?: boolean;
	expires_in_days?: number;
}): Promise<CreatedAPIKey> {
	return api.post<CreatedAPIKey>(`/api/api-keys`, data);
}

export function revokeAPIKey(id: string): Promise<void> {
	return api.delete<void>(`/api/api-keys/${id}`);
}

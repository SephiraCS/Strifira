import { api } from './client';
import type { CreateScreenShareRequest, ScreenShare, ScreenShareJoin, ScreenShareList } from '$lib/types/share';

export function listShares(slug: string): Promise<ScreenShareList> {
	return api.get<ScreenShareList>(`/api/workspaces/${slug}/shares`);
}

export function getShare(slug: string, id: string): Promise<ScreenShare> {
	return api.get<ScreenShare>(`/api/workspaces/${slug}/shares/${id}`);
}

export function createShare(slug: string, data: CreateScreenShareRequest): Promise<ScreenShareJoin> {
	return api.post<ScreenShareJoin>(`/api/workspaces/${slug}/shares`, data);
}

export function endShare(slug: string, id: string, presenterToken: string): Promise<void> {
	return api.post<void>(`/api/workspaces/${slug}/shares/${id}/end`, { presenter_token: presenterToken });
}

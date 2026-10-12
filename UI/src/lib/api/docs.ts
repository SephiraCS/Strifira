import { api } from './client';
import type {
	AssetFileList,
	CreateDocRequest,
	Doc,
	DocBacklink,
	DocComment,
	DocGraph,
	DocRevision,
	DocTag,
	DocTreeNode,
	UpdateDocRequest
} from '$lib/types/docs';

export function listDocuments(slug: string): Promise<DocTreeNode[]> {
	return api.get<DocTreeNode[]>(`/api/workspaces/${slug}/docs`);
}

export function getDocument(slug: string, id: string): Promise<Doc> {
	return api.get<Doc>(`/api/workspaces/${slug}/docs/${id}`);
}

export function createDocument(slug: string, req: CreateDocRequest): Promise<Doc> {
	return api.post<Doc>(`/api/workspaces/${slug}/docs`, req);
}

export function updateDocument(slug: string, id: string, req: UpdateDocRequest): Promise<Doc> {
	return api.patch<Doc>(`/api/workspaces/${slug}/docs/${id}`, req);
}

export function deleteDocument(slug: string, id: string): Promise<{ ok: boolean }> {
	return api.delete<{ ok: boolean }>(`/api/workspaces/${slug}/docs/${id}`);
}

export function restoreDocument(slug: string, id: string): Promise<Doc> {
	return api.post<Doc>(`/api/workspaces/${slug}/docs/${id}/restore`, {});
}

export function purgeDocument(slug: string, id: string): Promise<{ ok: boolean }> {
	return api.delete<{ ok: boolean }>(`/api/workspaces/${slug}/docs/${id}/purge`);
}

export function moveDocument(
	slug: string,
	id: string,
	parentId: string | null,
	position?: number
): Promise<Doc> {
	return api.post<Doc>(`/api/workspaces/${slug}/docs/${id}/move`, {
		parent_id: parentId,
		position
	});
}

export function duplicateDocument(slug: string, id: string): Promise<Doc> {
	return api.post<Doc>(`/api/workspaces/${slug}/docs/${id}/duplicate`, {});
}

export function searchDocuments(slug: string, q: string): Promise<DocTreeNode[]> {
	return api.get<DocTreeNode[]>(`/api/workspaces/${slug}/docs/search?q=${encodeURIComponent(q)}`);
}

export function listTrash(slug: string): Promise<DocTreeNode[]> {
	return api.get<DocTreeNode[]>(`/api/workspaces/${slug}/docs/trash`);
}

export function listRevisions(slug: string, id: string): Promise<DocRevision[]> {
	return api.get<DocRevision[]>(`/api/workspaces/${slug}/docs/${id}/revisions`);
}

export function restoreRevision(slug: string, id: string, revisionId: string): Promise<Doc> {
	return api.post<Doc>(`/api/workspaces/${slug}/docs/${id}/revisions/${revisionId}/restore`, {});
}

export function listBacklinks(slug: string, id: string): Promise<DocBacklink[]> {
	return api.get<DocBacklink[]>(`/api/workspaces/${slug}/docs/${id}/backlinks`);
}

export function documentGraph(slug: string): Promise<DocGraph> {
	return api.get<DocGraph>(`/api/workspaces/${slug}/docs/graph`);
}

export function listDocumentTags(slug: string): Promise<DocTag[]> {
	return api.get<DocTag[]>(`/api/workspaces/${slug}/doc-tags`);
}

export function setDocumentTags(slug: string, id: string, tags: string[]): Promise<DocTag[]> {
	return api.put<DocTag[]>(`/api/workspaces/${slug}/docs/${id}/tags`, { tags });
}

export function listDocumentComments(slug: string, id: string): Promise<DocComment[]> {
	return api.get<DocComment[]>(`/api/workspaces/${slug}/docs/${id}/comments`);
}

export function createDocumentComment(
	slug: string,
	id: string,
	body: string,
	parentId?: string
): Promise<DocComment> {
	return api.post<DocComment>(`/api/workspaces/${slug}/docs/${id}/comments`, {
		body,
		parent_id: parentId
	});
}

export function resolveDocumentComment(
	slug: string,
	id: string,
	commentId: string
): Promise<{ ok: boolean }> {
	return api.post<{ ok: boolean }>(`/api/workspaces/${slug}/docs/${id}/comments/${commentId}/resolve`, {});
}

export function reopenDocumentComment(
	slug: string,
	id: string,
	commentId: string
): Promise<{ ok: boolean }> {
	return api.post<{ ok: boolean }>(`/api/workspaces/${slug}/docs/${id}/comments/${commentId}/reopen`, {});
}

// --- Files ---

export function listFiles(
	slug: string,
	params?: { search?: string; page?: number; per_page?: number }
): Promise<AssetFileList> {
	const query = params ? '?' + new URLSearchParams(
		Object.entries(params)
			.filter(([, v]) => v !== undefined)
			.map(([k, v]) => [k, String(v)])
	).toString() : '';
	return api.get<AssetFileList>(`/api/workspaces/${slug}/assets${query}`);
}

export function deleteFile(slug: string, id: string): Promise<{ ok: boolean }> {
	return api.delete<{ ok: boolean }>(`/api/workspaces/${slug}/assets/${id}`);
}

export function shareFile(
	slug: string,
	id: string,
	ttl: '1h' | '24h' | '7d' | '30d' | '365d'
): Promise<{ url: string; expires_at: string }> {
	return api.post<{ url: string; expires_at: string }>(`/api/workspaces/${slug}/assets/${id}/share`, { ttl });
}

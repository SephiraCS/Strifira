export interface DocTreeNode {
	id: string;
	parent_id?: string;
	title: string;
	icon?: string;
	position: number;
	is_template: boolean;
	updated_at: string;
	tags: string[];
}

export interface Doc {
	id: string;
	workspace_id: string;
	parent_id?: string;
	title: string;
	icon?: string;
	content_html: string;
	content_text: string;
	position: number;
	is_template: boolean;
	created_by: string;
	updated_by?: string;
	created_at: string;
	updated_at: string;
	tags: string[];
}

export interface DocRevision {
	id: string;
	document_id: string;
	title: string;
	icon?: string;
	content_html: string;
	content_text: string;
	edited_by?: string;
	created_at: string;
}

export interface DocBacklink {
	id: string;
	title: string;
	icon?: string;
	updated_at: string;
}

export interface DocGraph {
	nodes: { id: string; title: string; icon?: string }[];
	edges: { source: string; target: string }[];
}

export interface DocTag {
	id: string;
	name: string;
}

export interface DocComment {
	id: string;
	document_id: string;
	user_id: string;
	body: string;
	parent_id?: string;
	resolved_at?: string;
	created_at: string;
	updated_at: string;
	replies?: DocComment[];
}

export interface CreateDocRequest {
	title?: string;
	icon?: string;
	parent_id?: string;
	content_md?: string;
	is_template?: boolean;
	tags?: string[];
}

export interface UpdateDocRequest {
	title?: string;
	icon?: string;
	content_html?: string;
	content_text?: string;
	wiki_titles?: string[];
	is_template?: boolean;
}

export interface AssetFileEntry {
	id: string;
	filename: string;
	content_type: string;
	size: number;
	uploaded_by: string;
	uploader_name: string;
	created_at: string;
	url: string;
}

export interface AssetFileList {
	items: AssetFileEntry[];
	total: number;
	page: number;
	per_page: number;
}

/** A peer currently connected to a document's collab session. */
export interface DocCollabPeer {
	client_id: string;
	user_id: string;
	name: string;
	color: string;
	anchor?: string;
	head?: string;
}

/** Live status of the collaborative editing connection. */
export type DocSyncStatus = 'connecting' | 'live' | 'offline';

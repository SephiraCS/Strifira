export interface ScreenShareUser {
	id: string;
	name: string;
	display_name: string;
	avatar_url: string | null;
}

export interface ScreenShare {
	id: string;
	workspace_id: string;
	started_by: string;
	started_by_user?: ScreenShareUser | null;
	title: string;
	room_name: string;
	status: 'active' | 'ended';
	viewer_count: number;
	created_at: string;
	ended_at: string | null;
}

export interface ScreenShareList {
	active: ScreenShare[];
	recent: ScreenShare[];
}

export interface ScreenShareJoin {
	share: ScreenShare;
	token: string;
	server_url: string;
}

export interface CreateScreenShareRequest {
	title?: string;
}

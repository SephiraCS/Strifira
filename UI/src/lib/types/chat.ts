export interface ChatUser {
	id: string;
	name: string;
	display_name: string;
	email: string;
	avatar_url: string | null;
}

export type ChatChannelType = 'public' | 'private' | 'dm';

export interface ChatChannel {
	id: string;
	workspace_id: string;
	type: ChatChannelType;
	name: string;
	topic: string;
	dm_peer?: ChatUser | null;
	created_by: string;
	created_at: string;
	updated_at: string;
	last_post_at?: string | null;
}

export interface ChatMessage {
	id: string;
	channel_id: string;
	user_id: string;
	author?: ChatUser | null;
	body: string;
	created_at: string;
	edited_at?: string | null;
	deleted_at?: string | null;
}

export interface ChatMessageListResponse {
	messages: ChatMessage[];
	has_more: boolean;
}

export interface ChatChannelListResponse {
	channels: ChatChannel[];
}

export interface ChatPresenceResponse {
	online: string[];
}

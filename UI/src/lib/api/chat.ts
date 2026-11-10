import { api } from './client';
import type {
	ChatChannel,
	ChatChannelListResponse,
	ChatMessage,
	ChatMessageListResponse,
	ChatPresenceResponse
} from '$lib/types/chat';

export function listChatChannels(slug: string): Promise<ChatChannelListResponse> {
	return api.get<ChatChannelListResponse>(`/api/workspaces/${slug}/chat/channels`);
}

export function createChatChannel(
	slug: string,
	body: { name: string; type: 'public' | 'private' }
): Promise<ChatChannel> {
	return api.post<ChatChannel>(`/api/workspaces/${slug}/chat/channels`, body);
}

export function ensureChatDM(slug: string, userId: string): Promise<ChatChannel> {
	return api.post<ChatChannel>(`/api/workspaces/${slug}/chat/dm`, { user_id: userId });
}

export function listChatMessages(
	slug: string,
	channelId: string,
	before?: string
): Promise<ChatMessageListResponse> {
	const query = before ? `?before=${before}` : '';
	return api.get<ChatMessageListResponse>(
		`/api/workspaces/${slug}/chat/channels/${channelId}/messages${query}`
	);
}

export function createChatMessage(
	slug: string,
	channelId: string,
	body: string
): Promise<ChatMessage> {
	return api.post<ChatMessage>(`/api/workspaces/${slug}/chat/channels/${channelId}/messages`, {
		body
	});
}

export function updateChatMessage(
	slug: string,
	messageId: string,
	body: string
): Promise<ChatMessage> {
	return api.patch<ChatMessage>(`/api/workspaces/${slug}/chat/messages/${messageId}`, { body });
}

export function deleteChatMessage(slug: string, messageId: string): Promise<void> {
	return api.delete<void>(`/api/workspaces/${slug}/chat/messages/${messageId}`);
}

export function getChatPresence(slug: string): Promise<ChatPresenceResponse> {
	return api.get<ChatPresenceResponse>(`/api/workspaces/${slug}/chat/presence`);
}

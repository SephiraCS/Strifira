<script lang="ts">
	import { onMount } from 'svelte';
	import RichEditor from '$lib/components/shared/RichEditor.svelte';
	import Avatar from '$lib/components/shared/Avatar.svelte';
	import {
		createDocumentComment,
		listDocumentComments,
		resolveDocumentComment,
		reopenDocumentComment
	} from '$lib/api/docs';
	import type { DocComment } from '$lib/types/docs';
	import type { WorkspaceMember } from '$lib/types/workspace';
	import { authState } from '$lib/features/auth/auth.state.svelte';
	import { appToast } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import MessageSquare from '@lucide/svelte/icons/message-square';
	import Check from '@lucide/svelte/icons/check';
	import RotateCcw from '@lucide/svelte/icons/rotate-ccw';

	let {
		slug,
		docId,
		members = [],
		readonly = false
	}: { slug: string; docId: string; members?: WorkspaceMember[]; readonly?: boolean } = $props();

	let comments = $state<DocComment[]>([]);
	let body = $state('');
	let replyingTo = $state<string | null>(null);
	let replyBody = $state('');
	let submitting = $state(false);

	const uploadUrl = $derived(`/api/workspaces/${slug}/upload`);

	onMount(() => {
		void load();
		window.addEventListener('ws:doc-comment', handleCommentEvent);
		return () => window.removeEventListener('ws:doc-comment', handleCommentEvent);
	});

	function handleCommentEvent(e: Event) {
		const detail = (e as CustomEvent<{ document_id?: string }>).detail;
		if (detail?.document_id === docId) void load();
	}

	async function load() {
		try {
			comments = await listDocumentComments(slug, docId);
		} catch {
			comments = [];
		}
	}

	function memberName(userId: string): string {
		return members.find((mem) => mem.user_id === userId)?.name ?? m['docs.comments.unknown_user']();
	}

	function memberAvatar(userId: string): string | undefined {
		return members.find((mem) => mem.user_id === userId)?.avatar_url ?? undefined;
	}

	async function submit() {
		if (!body.trim() || submitting) return;
		submitting = true;
		try {
			await createDocumentComment(slug, docId, body);
			body = '';
			await load();
		} catch (err) {
			appToast.apiError(err, m['docs.comments.failed_create']());
		} finally {
			submitting = false;
		}
	}

	async function submitReply(parentId: string) {
		if (!replyBody.trim() || submitting) return;
		submitting = true;
		try {
			await createDocumentComment(slug, docId, replyBody, parentId);
			replyBody = '';
			replyingTo = null;
			await load();
		} catch (err) {
			appToast.apiError(err, m['docs.comments.failed_create']());
		} finally {
			submitting = false;
		}
	}

	async function toggleResolved(comment: DocComment) {
		try {
			if (comment.resolved_at) {
				await reopenDocumentComment(slug, docId, comment.id);
			} else {
				await resolveDocumentComment(slug, docId, comment.id);
			}
			await load();
		} catch (err) {
			appToast.apiError(err, m['docs.comments.failed_resolve']());
		}
	}
</script>

<section class="mt-10 border-t border-[var(--app-border)] pt-4" aria-label={m['docs.comments.title']()}>
	<h3 class="mb-3 flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-[var(--color-text-tertiary)]">
		<MessageSquare size={13} />
		{m['docs.comments.title']()} ({comments.length})
	</h3>

	<div class="space-y-4">
		{#each comments as comment (comment.id)}
			{@const resolved = Boolean(comment.resolved_at)}
			<article class="flex gap-2.5 {resolved ? 'opacity-60' : ''}">
				<Avatar name={memberName(comment.user_id)} avatarUrl={memberAvatar(comment.user_id)} size="sm" />
				<div class="min-w-0 flex-1">
					<div class="flex items-center gap-2">
						<span class="text-sm font-medium text-[var(--color-text-primary)]">
							{memberName(comment.user_id)}
						</span>
						<span class="text-xs text-[var(--color-text-tertiary)]">
							{new Date(comment.created_at).toLocaleString()}
						</span>
						{#if resolved}
							<span class="rounded bg-[var(--color-bg-tertiary)] px-1.5 py-0.5 text-[10px] text-[var(--color-text-tertiary)]">
								{m['docs.comments.resolved']()}
							</span>
						{/if}
						{#if !readonly}
							<button
								type="button"
								class="ml-auto flex items-center gap-1 text-xs text-[var(--color-text-tertiary)] hover:text-[var(--color-text-secondary)]"
								onclick={() => toggleResolved(comment)}
							>
								{#if resolved}
									<RotateCcw size={12} />
									{m['docs.comments.reopen']()}
								{:else}
									<Check size={12} />
									{m['docs.comments.resolve']()}
								{/if}
							</button>
						{/if}
					</div>
					<!-- eslint-disable-next-line svelte/no-at-html-tags -- sanitized server-side -->
					<div class="prose prose-invert prose-sm max-w-none text-sm text-[var(--color-text-secondary)]">
						{@html comment.body}
					</div>

					{#if comment.replies && comment.replies.length > 0}
						<div class="mt-2 space-y-2 border-l border-[var(--app-border)] pl-3">
							{#each comment.replies as reply (reply.id)}
								<div class="flex gap-2">
									<Avatar name={memberName(reply.user_id)} avatarUrl={memberAvatar(reply.user_id)} size="xs" />
									<div>
										<span class="text-xs font-medium text-[var(--color-text-primary)]">
											{memberName(reply.user_id)}
										</span>
										<!-- eslint-disable-next-line svelte/no-at-html-tags -->
										<div class="prose prose-invert prose-sm max-w-none text-sm text-[var(--color-text-secondary)]">
											{@html reply.body}
										</div>
									</div>
								</div>
							{/each}
						</div>
					{/if}

					{#if !readonly}
						{#if replyingTo === comment.id}
							<div class="mt-2">
								<RichEditor
									content={replyBody}
									minimal
									compact
									bubbleMenu
									placeholder={m['docs.comments.reply_placeholder']()}
									onupdate={(html) => (replyBody = html)}
									onsubmit={() => void submitReply(comment.id)}
									{uploadUrl}
									showAIButton={false}
								/>
								<div class="mt-1 flex gap-2">
									<button
										type="button"
										class="rounded-md bg-[var(--app-accent)] px-2.5 py-1 text-xs font-medium text-[var(--app-accent-foreground)] disabled:opacity-50"
										disabled={!replyBody.trim() || submitting}
										onclick={() => void submitReply(comment.id)}
									>
										{m['docs.comments.reply']()}
									</button>
									<button
										type="button"
										class="rounded-md px-2.5 py-1 text-xs text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)]"
										onclick={() => {
											replyingTo = null;
											replyBody = '';
										}}
									>
										{m['docs.cancel']()}
									</button>
								</div>
							</div>
						{:else}
							<button
								type="button"
								class="mt-1 text-xs text-[var(--color-text-tertiary)] hover:text-[var(--color-text-secondary)]"
								onclick={() => {
									replyingTo = comment.id;
									replyBody = '';
								}}
							>
								{m['docs.comments.reply']()}
							</button>
						{/if}
					{/if}
				</div>
			</article>
		{:else}
			<p class="text-sm text-[var(--color-text-tertiary)]">{m['docs.comments.empty']()}</p>
		{/each}
	</div>

	{#if !readonly}
		<div class="mt-4">
			<RichEditor
				content={body}
				minimal
				compact
				bubbleMenu
				placeholder={m['docs.comments.placeholder']()}
				onupdate={(html) => (body = html)}
				onsubmit={() => void submit()}
				{uploadUrl}
				{members}
				showAIButton={false}
			/>
			<div class="mt-1 flex justify-end">
				<button
					type="button"
					class="rounded-md bg-[var(--app-accent)] px-3 py-1.5 text-xs font-medium text-[var(--app-accent-foreground)] disabled:opacity-50"
					disabled={!body.trim() || submitting}
					onclick={() => void submit()}
				>
					{m['docs.comments.submit']()}
				</button>
			</div>
		</div>
	{/if}
</section>

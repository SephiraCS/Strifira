<script lang="ts">
	import DOMPurify from 'dompurify';
	import { api } from '$lib/api/client';
	import { m } from '$lib/paraglide/messages.js';
	import type { MailMessageDetail } from '$lib/types/mail';

	let { slug, message }: { slug: string; message: MailMessageDetail } = $props();
	let remoteBlocked = $state(false);
	let loadRemote = () => {};

	function renderBody(host: HTMLElement, input: { slug: string; message: MailMessageDetail }) {
		const root = host.shadowRoot ?? host.attachShadow({ mode: 'open' });
		let dispose = () => {};
		function render(value: typeof input) {
			dispose();
			let active = true;
			const pending = new AbortController();
			dispose = () => {
				active = false;
				pending.abort();
			};
			remoteBlocked = false;
			loadRemote = () => {};
			const purifier = DOMPurify(window);
			// Cached messages may still use src. Strip all network-capable attributes
			// before inserting anything in the live document, including hidden panes.
			purifier.addHook('beforeSanitizeAttributes', (node) => {
				if (node.nodeName === 'IMG' && !node.hasAttribute('data-mail-image')) {
					const source = node.getAttribute('src')?.trim() ?? '';
					node.setAttribute(
						'data-mail-image',
						source.startsWith('https://') ? source : source ? 'blocked:' : 'invalid:'
					);
				}
			});
			const safe = purifier.sanitize(value.message.body_html, {
				ALLOWED_TAGS: [
					'p',
					'br',
					'strong',
					'b',
					'em',
					'i',
					's',
					'del',
					'code',
					'pre',
					'a',
					'h1',
					'h2',
					'h3',
					'h4',
					'h5',
					'h6',
					'ul',
					'ol',
					'li',
					'blockquote',
					'hr',
					'span',
					'div',
					'table',
					'thead',
					'tbody',
					'tr',
					'th',
					'td',
					'img'
				],
				ALLOWED_ATTR: ['href', 'alt', 'width', 'height', 'colspan', 'rowspan', 'data-mail-image'],
				ALLOW_DATA_ATTR: false,
				ADD_ATTR: ['data-mail-image'],
				// This inert attribute carries CID descriptors, never a live URL attribute.
				ADD_URI_SAFE_ATTR: ['data-mail-image']
			});
			const style = document.createElement('style');
			style.textContent = `
    :host { display:block; min-width:0; max-width:100%; overflow-wrap:anywhere; color:inherit; font:inherit; }
    * { box-sizing:border-box; max-width:100%; }
    img { display:inline-block; max-width:100%!important; height:auto!important; vertical-align:middle; }
    p { margin:0 0 .75em; } h1,h2,h3 { margin:0 0 .6em; line-height:1.3; }
    table { display:block; overflow-x:auto; width:auto; } pre { white-space:pre-wrap; }
    a { color:inherit; text-decoration:underline; } blockquote { margin-inline:.5em; }
    .image-fallback { display:inline-block; padding:.4em .6em; margin:.2em 0; border:1px solid currentColor; border-radius:.3em; font-size:.85em; opacity:.8; }
   `;
			const body = document.createElement('div');
			body.innerHTML = safe;
			root.replaceChildren(style, body);
			for (const link of body.querySelectorAll('a')) {
				const href = link.getAttribute('href') ?? '';
				if (!/^(https:\/\/|mailto:)/i.test(href)) link.removeAttribute('href');
				link.target = '_blank';
				link.rel = 'noopener noreferrer';
				link.referrerPolicy = 'no-referrer';
			}
			const remote: (() => Promise<void>)[] = [];
			const loads = new Map<string, Promise<{ data_url: string }>>();
			for (const original of body.querySelectorAll('img')) {
				const source = original.getAttribute('data-mail-image') ?? 'invalid:';
				const alt = original.alt || m['mail.images.image']();
				const fallback = document.createElement('span');
				fallback.className = 'image-fallback';
				fallback.setAttribute('role', 'img');
				function describe(reason: string) {
					fallback.textContent = alt + ': ' + reason;
					fallback.setAttribute('aria-label', fallback.textContent);
				}
				original.replaceWith(fallback);
				const load = async () => {
					describe(m['mail.images.loading']());
					try {
						let request = loads.get(source);
						if (!request) {
							request = api.fetch<{ data_url: string }>(
								`/api/workspaces/${value.slug}/mail/messages/${value.message.id}/images`,
								{
									method: 'POST',
									body: JSON.stringify({ source }),
									signal: pending.signal
								}
							);
							loads.set(source, request);
						}
						const response = await request;
						if (!active) return;
						if (!/^data:image\/(png|jpeg|gif|webp);base64,/.test(response.data_url)) throw new Error('Invalid image');
						const image = document.createElement('img');
						image.alt = alt;
						const width = original.getAttribute('width');
						if (width && /^\d+$/.test(width)) image.width = Number(width);
						image.referrerPolicy = 'no-referrer';
						image.onerror = () => {
							if (active) {
								describe(m['mail.images.unavailable']());
								image.replaceWith(fallback);
							}
						};
						image.src = response.data_url;
						fallback.replaceWith(image);
					} catch (error) {
						if (!active) return;
						const code = (error as { error?: { code?: string } })?.error?.code;
						describe(
							code === 'MAIL_IMAGE_BLOCKED'
								? m['mail.images.blocked']()
								: code === 'MAIL_IMAGE_INVALID' || code === 'NOT_FOUND'
									? m['mail.images.invalid']()
									: m['mail.images.unavailable']()
						);
					}
				};
				if (source.startsWith('attachment:')) void load();
				else if (source.startsWith('https://')) {
					describe(m['mail.images.remote_blocked']());
					remote.push(load);
				} else describe(source === 'blocked:' ? m['mail.images.blocked']() : m['mail.images.invalid']());
			}
			remoteBlocked = remote.length > 0;
			loadRemote = () => {
				remoteBlocked = false;
				// Bound concurrent requests even for sender-controlled HTML with many images.
				void (async () => {
					for (const load of remote) {
						if (!active) return;
						await load();
					}
				})();
			};
		}
		render(input);
		return {
			update: render,
			destroy: () => {
				dispose();
				root.replaceChildren();
			}
		};
	}
</script>

{#if message.body_html}
	<div class="min-w-0 max-w-full text-sm text-[var(--color-text-primary)]">
		{#if remoteBlocked}
			<div class="mb-3 rounded-md border border-[var(--app-border)] p-3 text-xs">
				<p class="mb-2">{m['mail.images.privacy']()}</p>
				<button
					type="button"
					class="cursor-pointer rounded border border-[var(--app-border)] px-3 py-1.5 hover:bg-[var(--color-bg-hover)]"
					onclick={() => loadRemote()}
				>
					{m['mail.images.load_remote']()}
				</button>
			</div>
		{/if}
		<div use:renderBody={{ slug, message }}></div>
	</div>
{:else}
	<pre
		class="max-w-full font-sans text-sm whitespace-pre-wrap wrap-anywhere text-[var(--color-text-primary)]">{message.body_text}</pre>
{/if}

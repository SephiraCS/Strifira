<script lang="ts">
	import { AlertDialog } from 'bits-ui';
	import { m } from '$lib/paraglide/messages.js';

	import { tick } from 'svelte';

	/**
	 * A confirmation prompt, shown while it is mounted. Built on the bits-ui
	 * alert dialog, which traps Tab inside the dialog and closes on Escape.
	 *
	 * `onconfirm` may return a promise (e.g. the DELETE request): the dialog
	 * then stays open, ignoring further clicks and Escape, until the caller
	 * unmounts it once the operation has finished and its result is rendered.
	 * On unmount focus returns to the element that opened the dialog or, when
	 * that element is gone (its row was deleted), to the nearest surviving
	 * control after or before it, preferring one like it (the next row's
	 * delete button).
	 */
	let {
		title,
		message,
		confirmLabel,
		variant = 'danger',
		onconfirm,
		oncancel
	}: {
		title: string;
		message: string;
		confirmLabel?: string;
		variant?: 'danger' | 'default';
		onconfirm: () => void | Promise<unknown>;
		oncancel: () => void;
	} = $props();

	const effectiveConfirmLabel = $derived(confirmLabel ?? m['sharedComponents.confirm_dialog.confirm']());
	let cancelButton = $state<HTMLButtonElement | null>(null);
	let busy = $state(false);

	async function confirm() {
		if (busy) return;
		const result = onconfirm();
		if (!(result instanceof Promise)) return;
		busy = true;
		try {
			await result;
		} catch {
			// The caller reports its own failure.
		} finally {
			busy = false;
		}
	}

	const focusableSelector =
		'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

	// The element that opened the dialog and the page's focusable elements in
	// document order, captured before the dialog renders and takes focus.
	const opener = typeof document === 'undefined' ? null : document.activeElement;
	const pageFocusables =
		typeof document === 'undefined' ? [] : [...document.querySelectorAll<HTMLElement>(focusableSelector)];
	const openerIndex = opener instanceof HTMLElement ? pageFocusables.indexOf(opener) : -1;

	function isFocusTarget(el: HTMLElement) {
		return el.isConnected && !el.matches(':disabled') && el.getClientRects().length > 0;
	}

	function survivingTarget(): HTMLElement | undefined {
		if (openerIndex < 0 || !(opener instanceof HTMLElement)) return undefined;
		// Nearest first: everything after the opener, then everything before it.
		const ordered = [...pageFocusables.slice(openerIndex + 1), ...pageFocusables.slice(0, openerIndex).reverse()];
		const title = opener.getAttribute('title');
		const alike = (el: HTMLElement) => el.tagName === opener.tagName && !!title && el.getAttribute('title') === title;
		return ordered.find((el) => alike(el) && isFocusTarget(el)) ?? ordered.find(isFocusTarget);
	}

	function focusLost() {
		const active = document.activeElement;
		return !active || active === document.body || !active.isConnected;
	}

	// The dialog has no AlertDialog.Trigger, so focus is returned here rather
	// than left to bits-ui's default close autofocus.
	function restoreFocus(event: Event) {
		event.preventDefault();
		if (opener instanceof HTMLElement && opener.isConnected) opener.focus();
		// The caller may remove the opener in the same update that closes the
		// dialog; check again once the DOM has settled.
		void tick().then(() => {
			if (opener instanceof HTMLElement && opener.isConnected) {
				if (focusLost()) opener.focus();
				return;
			}
			if (focusLost()) survivingTarget()?.focus();
		});
	}
</script>

<!-- Controlled: the dialog is open while mounted, and closes only when the
     caller unmounts it, so Escape cannot hide it while a confirm is busy. -->
<AlertDialog.Root
	bind:open={
		() => true,
		(open) => {
			if (!open && !busy) oncancel();
		}
	}
>
	<AlertDialog.Portal>
		<AlertDialog.Overlay class="fixed inset-0 z-50 bg-black/50" />
		<AlertDialog.Content
			interactOutsideBehavior="close"
			onOpenAutoFocus={(e) => {
				e.preventDefault();
				cancelButton?.focus();
			}}
			onCloseAutoFocus={restoreFocus}
			class="fixed top-1/2 left-1/2 z-50 w-full max-w-sm -translate-x-1/2 -translate-y-1/2 rounded-lg border border-[var(--app-border)] bg-[var(--color-bg-secondary)] p-6 shadow-2xl outline-none"
		>
			<AlertDialog.Title level={3} class="text-sm font-medium text-[var(--color-text-primary)]">{title}</AlertDialog.Title>
			<AlertDialog.Description class="mt-2 text-sm text-[var(--color-text-secondary)]">{message}</AlertDialog.Description>
			<div class="mt-4 flex justify-end gap-2">
				<AlertDialog.Cancel
					bind:ref={cancelButton}
					aria-disabled={busy}
					class="rounded-md border border-[var(--app-border)] aria-disabled:opacity-60 px-3 py-1.5 text-sm text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)]"
				>
					{m['sharedComponents.confirm_dialog.cancel']()}
				</AlertDialog.Cancel>
				<AlertDialog.Action
					onclick={confirm}
					aria-disabled={busy}
					aria-busy={busy}
					class="rounded-md px-3 aria-disabled:opacity-60 py-1.5 text-sm text-white {variant === 'danger'
						? 'bg-[var(--color-error)] hover:bg-red-600'
						: 'bg-[var(--app-accent)] hover:bg-[var(--app-accent-hover)]'}"
				>
					{effectiveConfirmLabel}
				</AlertDialog.Action>
			</div>
		</AlertDialog.Content>
	</AlertDialog.Portal>
</AlertDialog.Root>

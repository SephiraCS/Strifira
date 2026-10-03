<script lang="ts">
	import { AlertDialog } from 'bits-ui';
	import { m } from '$lib/paraglide/messages.js';

	/**
	 * A confirmation prompt, shown while it is mounted. Built on the bits-ui
	 * alert dialog, which traps Tab inside the dialog and closes on Escape; it
	 * returns focus to the element that opened it when it unmounts.
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
		onconfirm: () => void;
		oncancel: () => void;
	} = $props();

	const effectiveConfirmLabel = $derived(confirmLabel ?? m['sharedComponents.confirm_dialog.confirm']());
	let cancelButton = $state<HTMLButtonElement | null>(null);

	// The element that opened the dialog and its ancestors, captured before the
	// dialog takes focus. When the opener is gone on close (a confirmed delete
	// removes its row), focus moves into the nearest surviving ancestor rather
	// than falling back to the document body.
	const opener = typeof document === 'undefined' ? null : document.activeElement;
	const openerAncestors: Element[] = [];
	for (let el = opener?.parentElement; el && el !== document.body; el = el.parentElement) {
		openerAncestors.push(el);
	}

	const focusableSelector =
		'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

	// The dialog has no AlertDialog.Trigger, so focus is returned here rather
	// than left to bits-ui's default close autofocus.
	function restoreFocus(event: Event) {
		event.preventDefault();
		if (opener instanceof HTMLElement && opener.isConnected) {
			opener.focus();
			return;
		}
		const container = openerAncestors.find((el) => el.isConnected);
		container?.querySelector<HTMLElement>(focusableSelector)?.focus();
	}
</script>

<AlertDialog.Root
	open
	onOpenChange={(open) => {
		if (!open) oncancel();
	}}
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
					class="rounded-md border border-[var(--app-border)] px-3 py-1.5 text-sm text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-hover)]"
				>
					{m['sharedComponents.confirm_dialog.cancel']()}
				</AlertDialog.Cancel>
				<AlertDialog.Action
					onclick={onconfirm}
					class="rounded-md px-3 py-1.5 text-sm text-white {variant === 'danger'
						? 'bg-[var(--color-error)] hover:bg-red-600'
						: 'bg-[var(--app-accent)] hover:bg-[var(--app-accent-hover)]'}"
				>
					{effectiveConfirmLabel}
				</AlertDialog.Action>
			</div>
		</AlertDialog.Content>
	</AlertDialog.Portal>
</AlertDialog.Root>

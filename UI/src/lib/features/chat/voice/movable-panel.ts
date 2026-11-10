/** Position belongs to this mounted call panel, independently of the media session. */
export function movableVoicePanel(panel: HTMLElement) {
	const handle = panel.querySelector<HTMLButtonElement>('[data-voice-drag-handle]')!;
	let position: { x: number; y: number } | undefined;
	let drag: { id: number; x: number; y: number; left: number; top: number } | undefined;

	function bounds() {
		const viewport = window.visualViewport;
		const left = (viewport?.offsetLeft ?? 0) + 16;
		const top = (viewport?.offsetTop ?? 0) + 16;
		const width = Math.max(0, (viewport?.width ?? window.innerWidth) - 32);
		const height = Math.max(0, (viewport?.height ?? window.innerHeight) - 32);
		panel.style.maxWidth = `${width}px`;
		panel.style.maxHeight = `${height}px`;
		return {
			left,
			top,
			right: left + Math.max(0, width - panel.offsetWidth),
			bottom: top + Math.max(0, height - panel.offsetHeight)
		};
	}

	function place(x?: number, y?: number) {
		const limits = bounds();
		position = {
			x: Math.max(limits.left, Math.min(limits.right, x ?? limits.left)),
			y: Math.max(limits.top, Math.min(limits.bottom, y ?? limits.bottom))
		};
		panel.style.left = `${position.x}px`;
		panel.style.top = `${position.y}px`;
		panel.style.bottom = 'auto';
	}

	function fit() {
		place(position?.x, position?.y);
	}

	function start(event: PointerEvent) {
		if (!event.isPrimary || event.button !== 0 || drag) return;
		event.preventDefault();
		handle.focus({ preventScroll: true });
		const rect = panel.getBoundingClientRect();
		drag = { id: event.pointerId, x: event.clientX, y: event.clientY, left: rect.left, top: rect.top };
		handle.setPointerCapture(event.pointerId);
		handle.style.cursor = 'grabbing';
	}

	function move(event: PointerEvent) {
		if (!drag || event.pointerId !== drag.id) return;
		place(drag.left + event.clientX - drag.x, drag.top + event.clientY - drag.y);
	}

	function end(event?: PointerEvent) {
		if (!drag || (event && event.pointerId !== drag.id)) return;
		const id = drag.id;
		drag = undefined;
		if (handle.hasPointerCapture(id)) handle.releasePointerCapture(id);
		handle.style.cursor = '';
	}

	function keydown(event: KeyboardEvent) {
		if (event.altKey || event.ctrlKey || event.metaKey) return;
		const step = event.shiftKey ? 48 : 12;
		const delta: Record<string, [number, number]> = {
			ArrowLeft: [-step, 0],
			ArrowRight: [step, 0],
			ArrowUp: [0, -step],
			ArrowDown: [0, step]
		};
		if (event.key === 'Escape' && drag) {
			place(drag.left, drag.top);
			end();
		} else if (event.key === 'Home') {
			place();
		} else if (delta[event.key] && !drag) {
			place(position!.x + delta[event.key][0], position!.y + delta[event.key][1]);
		} else if (event.key !== ' ' && event.key !== 'Enter') {
			return;
		}
		// Repositioning must not trigger application shortcuts or push-to-talk.
		event.preventDefault();
		event.stopPropagation();
	}

	fit();
	const observer = new ResizeObserver(fit);
	observer.observe(panel);
	window.addEventListener('resize', fit);
	window.visualViewport?.addEventListener('resize', fit);
	window.visualViewport?.addEventListener('scroll', fit);
	handle.addEventListener('pointerdown', start);
	handle.addEventListener('pointermove', move);
	handle.addEventListener('pointerup', end);
	handle.addEventListener('pointercancel', end);
	handle.addEventListener('lostpointercapture', end);
	handle.addEventListener('keydown', keydown);

	return {
		destroy() {
			end();
			observer.disconnect();
			window.removeEventListener('resize', fit);
			window.visualViewport?.removeEventListener('resize', fit);
			window.visualViewport?.removeEventListener('scroll', fit);
			handle.removeEventListener('pointerdown', start);
			handle.removeEventListener('pointermove', move);
			handle.removeEventListener('pointerup', end);
			handle.removeEventListener('pointercancel', end);
			handle.removeEventListener('lostpointercapture', end);
			handle.removeEventListener('keydown', keydown);
		}
	};
}

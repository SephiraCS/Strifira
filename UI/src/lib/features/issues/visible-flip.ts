import { flip, type AnimationConfig, type FlipParams } from 'svelte/animate';

/** Skip invisible siblings before FLIP reads their styles or creates an effect. */
export function visibleFlip(
	node: Element,
	position: { from: DOMRect; to: DOMRect },
	params: FlipParams
): AnimationConfig {
	if (params.duration === 0) return { duration: 0 };
	const visible = (rect: DOMRect) =>
		rect.right > 0 && rect.left < window.innerWidth && rect.bottom > 0 && rect.top < window.innerHeight;
	if (!visible(position.from) && !visible(position.to)) return { duration: 0 };
	return flip(node, position, params);
}

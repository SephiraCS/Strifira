import { getAISettings } from '$lib/api/ai-settings';

/**
 * Whether the workspace has a usable AI configuration. Members can read the
 * (non-secret) AI settings, so every AI affordance can hide or disable itself
 * with a helpful hint instead of failing on click.
 */
class AISettingsState {
	configured = $state<Record<string, boolean>>({});

	isConfigured(slug: string): boolean {
		// Optimistic until the first load answers; a transient failure must
		// never hide the button.
		return this.configured[slug] ?? true;
	}

	async load(slug: string) {
		if (!slug || this.configured[slug] !== undefined) return;
		try {
			const settings = await getAISettings(slug);
			this.configured[slug] =
				!!settings.base_url && !!settings.model && settings.has_api_key;
		} catch {
			this.configured[slug] = true;
		}
	}

	markConfigured(slug: string, configured: boolean) {
		this.configured[slug] = configured;
	}

	clear() {
		this.configured = {};
	}
}

export const aiSettingsState = new AISettingsState();

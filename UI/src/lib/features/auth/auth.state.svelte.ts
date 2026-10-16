import type { User } from '$lib/types/auth';
import { getMe } from '$lib/api/auth';

class AuthState {
	user = $state<User | null>(null);
	loading = $state(true);
	authenticated = $derived(this.user !== null);
	#probed = false;

	async init() {
		// One /me probe per app lifetime: init() runs on every authed page
		// mount, and the session itself only changes through setUser/clear.
		if (this.#probed) return;
		this.#probed = true;
		try {
			this.user = await getMe();
		} catch {
			this.user = null;
		} finally {
			this.loading = false;
		}
	}

	setUser(user: User | null) {
		this.user = user;
	}

	clear() {
		this.user = null;
	}
}

export const authState = new AuthState();

<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { login, register } from '$lib/api/auth';
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import ChevronUp from '@lucide/svelte/icons/chevron-up';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import { Input } from '$lib/components/ui/input';
	import { Password } from '$lib/components/ui/password';
	import { authState } from '$lib/features/auth/auth.state.svelte';
	import { listWorkspaces } from '$lib/api/workspaces';
	import { demoMode, demoUsers, type DemoUser } from '$lib/demo';
	import { appToast, getErrorMessage } from '$lib/features/toast/toast';
	import { m } from '$lib/paraglide/messages.js';
	import { getLocale, setLocale, locales } from '$lib/paraglide/runtime.js';

	let { mode }: { mode: 'login' | 'register' } = $props();

	let email = $state('');
	let password = $state('');
	let name = $state('');
	let loading = $state(false);
	let demoDrawerOpen = $state(false);
	let capsLockOn = $state(false);
	let formError = $state('');
	let fieldErrors = $state<{ name?: string; email?: string; password?: string }>({});
	let selectedLocale = $state(getLocale());

	let nameInput = $state<HTMLInputElement | null>(null);
	let emailInput = $state<HTMLInputElement | null>(null);
	let passwordInput = $state<HTMLInputElement | null>(null);

	const LOCALE_LABELS: Record<string, string> = {
		en: 'English',
		it: 'Italiano',
		'pt-BR': 'Português (Brasil)',
		'zh-CN': '简体中文',
		'zh-TW': '繁體中文'
	};

	const authInputClass =
		'mt-1 h-auto w-full rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-3 py-2 text-sm text-[var(--color-text-primary)] outline-none focus-visible:border-[var(--app-accent)] focus-visible:ring-[color-mix(in_srgb,var(--app-accent)_30%,transparent)]';

	async function redirectAfterAuth() {
		const workspaces = await listWorkspaces();
		if (workspaces.length > 0) {
			goto(`/${workspaces[0].slug}/inbox`);
		} else {
			goto('/workspace-setup');
		}
	}

	onMount(() => {
		// Signed-in users never see the auth forms.
		authState.init().then(() => {
			if (authState.authenticated) redirectAfterAuth().catch(() => {});
		});
	});

	function passwordStrength(pw: string): number {
		let score = 0;
		if (pw.length >= 8) score++;
		if (pw.length >= 12) score++;
		if (/[a-z]/.test(pw) && /[A-Z]/.test(pw)) score++;
		if (/\d/.test(pw)) score++;
		if (/[^A-Za-z0-9]/.test(pw)) score++;
		return Math.min(score, 4);
	}

	const strength = $derived(mode === 'register' && password ? passwordStrength(password) : 0);
	const strengthLabel = $derived(
		strength <= 1
			? m['login.strength.weak']()
			: strength === 2
				? m['login.strength.fair']()
				: strength === 3
					? m['login.strength.good']()
					: m['login.strength.strong']()
	);
	const strengthColor = $derived(
		strength <= 1
			? 'bg-[var(--color-error)]'
			: strength === 2
				? 'bg-[var(--color-warning)]'
				: 'bg-[var(--color-success)]'
	);

	function validate(): boolean {
		const next: typeof fieldErrors = {};
		if (mode === 'register' && !name.trim()) next.name = m['login.error.name_required']();
		if (!email.trim()) next.email = m['login.error.email_required']();
		else if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim()))
			next.email = m['login.error.email_invalid']();
		if (!password) next.password = m['login.error.password_required']();
		else if (mode === 'register' && password.length < 8)
			next.password = m['login.error.password_min']();
		fieldErrors = next;
		return !next.name && !next.email && !next.password;
	}

	function clearFieldError(key: 'name' | 'email' | 'password') {
		if (fieldErrors[key]) fieldErrors = { ...fieldErrors, [key]: undefined };
	}

	function checkCapsLock(e: KeyboardEvent) {
		capsLockOn = e.getModifierState('CapsLock');
	}

	async function handleSubmit(e: Event) {
		e.preventDefault();
		formError = '';
		if (!validate()) {
			const firstInvalid = nameInput && fieldErrors.name ? nameInput : fieldErrors.email ? emailInput : passwordInput;
			firstInvalid?.focus();
			return;
		}
		loading = true;

		try {
			const user =
				mode === 'login'
					? await login({ email, password })
					: await register({ email, password, name });
			authState.setUser(user);
			await redirectAfterAuth();
		} catch (err: unknown) {
			formError = getErrorMessage(err, m['login.error_generic']());
			appToast.apiError(err, m['login.error_generic']());
		} finally {
			loading = false;
		}
	}

	function useDemoUser(user: DemoUser) {
		email = user.email;
		password = user.password;
		demoDrawerOpen = false;
	}
</script>

<svelte:head>
	<title>{mode === 'login' ? m['login.title.signin']() : m['login.title.register']()}</title>
</svelte:head>

<div class="relative flex min-h-screen items-center justify-center overflow-hidden bg-[var(--color-bg)]">
	<div aria-hidden="true" class="pointer-events-none absolute inset-0">
		<div
			class="absolute top-1/2 left-1/2 h-[42rem] w-[42rem] -translate-x-1/2 -translate-y-[62%] rounded-full bg-[radial-gradient(closest-side,color-mix(in_srgb,var(--app-accent)_18%,transparent),transparent)] blur-2xl"
		></div>
		<div
			class="absolute top-1/2 left-1/2 h-[24rem] w-[46rem] -translate-x-1/2 -translate-y-[28%] rounded-full bg-[radial-gradient(closest-side,color-mix(in_srgb,var(--app-accent-light)_12%,transparent),transparent)] blur-2xl"
		></div>
	</div>

	<div class="absolute top-4 right-4 z-10 sm:top-6 sm:right-6">
		<label for="locale-select" class="sr-only">{m['login.language']()}</label>
		<select
			id="locale-select"
			bind:value={selectedLocale}
			onchange={() => setLocale(selectedLocale as (typeof locales)[number])}
			class="rounded-md border border-[var(--app-border)] bg-[var(--color-bg-secondary)] px-2 py-1 text-xs text-[var(--color-text-secondary)] outline-none hover:border-[var(--app-border-hover)] focus-visible:border-[var(--app-accent)] focus-visible:ring-[color-mix(in_srgb,var(--app-accent)_30%,transparent)]"
		>
			{#each locales as locale (locale)}
				<option value={locale}>{LOCALE_LABELS[locale] ?? locale}</option>
			{/each}
		</select>
	</div>

	<div class="relative z-10 w-full max-w-sm space-y-6 p-8">
		<div class="text-center">
			<div
				role="img"
				aria-label={m['login.logo_alt']()}
				class="mx-auto mb-4 aspect-[1438/838] w-44 bg-contain bg-center bg-no-repeat bg-[url(/logo-vertical.svg)] dark:bg-[url(/logo-vertical-white.svg)]"
			></div>
			<h1 class="sr-only">Strifira</h1>
			<p class="mt-1 text-sm text-[var(--color-text-secondary)]">
				{mode === 'login' ? m['login.subtitle.signin']() : m['login.subtitle.register']()}
			</p>
		</div>

		<form onsubmit={handleSubmit} novalidate class="space-y-4">
			{#if mode === 'register'}
				<div>
					<label for="name" class="block text-sm text-[var(--color-text-secondary)]">{m['login.name']()}</label>
					<Input
						id="name"
						name="name"
						type="text"
						autocomplete="name"
						bind:value={name}
						bind:ref={nameInput}
						aria-invalid={fieldErrors.name ? 'true' : undefined}
						aria-describedby={fieldErrors.name ? 'name-error' : undefined}
						oninput={() => clearFieldError('name')}
						class={authInputClass}
					/>
					{#if fieldErrors.name}
						<p id="name-error" class="mt-1 text-xs text-[var(--color-error)]">{fieldErrors.name}</p>
					{/if}
				</div>
			{/if}

			<div>
				<label for="email" class="block text-sm text-[var(--color-text-secondary)]">{m['login.email']()}</label>
				<Input
					id="email"
					name="email"
					type="email"
					autocomplete="email"
					inputmode="email"
					autocapitalize="none"
					spellcheck="false"
					enterkeyhint="next"
					placeholder={m['login.email.placeholder']()}
					bind:value={email}
					bind:ref={emailInput}
					aria-invalid={fieldErrors.email ? 'true' : undefined}
					aria-describedby={fieldErrors.email ? 'email-error' : undefined}
					oninput={() => clearFieldError('email')}
					class={authInputClass}
				/>
				{#if fieldErrors.email}
					<p id="email-error" class="mt-1 text-xs text-[var(--color-error)]">{fieldErrors.email}</p>
				{/if}
			</div>

			<div>
				<label for="password" class="block text-sm text-[var(--color-text-secondary)]">{m['login.password']()}</label>
				<Password
					id="password"
					name="password"
					autocomplete={mode === 'login' ? 'current-password' : 'new-password'}
					minlength={mode === 'register' ? 8 : undefined}
					enterkeyhint={mode === 'login' ? 'go' : 'done'}
					bind:value={password}
					bind:ref={passwordInput}
					onkeydown={checkCapsLock}
					onkeyup={checkCapsLock}
					onblur={() => (capsLockOn = false)}
					aria-invalid={fieldErrors.password ? 'true' : undefined}
					aria-describedby={fieldErrors.password ? 'password-error' : undefined}
					oninput={() => clearFieldError('password')}
					class={authInputClass}
				/>
				{#if fieldErrors.password}
					<p id="password-error" class="mt-1 text-xs text-[var(--color-error)]">{fieldErrors.password}</p>
				{:else if capsLockOn}
					<p class="mt-1 text-xs text-[var(--color-warning)]" role="status">{m['login.capslock']()}</p>
				{:else if mode === 'register' && !password}
					<p class="mt-1 text-xs text-[var(--color-text-tertiary)]">{m['login.password.hint']()}</p>
				{/if}

				{#if mode === 'register' && password}
					<div class="mt-2" aria-live="polite">
						<div class="flex gap-1">
							{#each [1, 2, 3, 4] as segment (segment)}
								<div
									class="h-1 flex-1 rounded-full transition-colors {strength >= segment
										? strengthColor
										: 'bg-[var(--color-bg-tertiary)]'}"
								></div>
							{/each}
						</div>
						<p class="mt-1 text-xs text-[var(--color-text-tertiary)]">
							{m['login.strength']()}: <span class="text-[var(--color-text-secondary)]">{strengthLabel}</span>
						</p>
					</div>
				{/if}
			</div>

			{#if formError}
				<p
					role="alert"
					class="rounded-md border border-[color-mix(in_srgb,var(--color-error)_40%,transparent)] bg-[color-mix(in_srgb,var(--color-error)_10%,transparent)] px-3 py-2 text-xs text-[var(--color-error)]"
				>
					{formError}
				</p>
			{/if}

			<button
				type="submit"
				disabled={loading}
				aria-busy={loading}
				class="inline-flex w-full items-center justify-center gap-2 rounded-md bg-[var(--app-accent)] px-4 py-2 text-sm font-medium text-[var(--app-accent-foreground)] hover:bg-[var(--app-accent-hover)] disabled:opacity-50"
			>
				{#if loading}
					<LoaderCircle size={14} class="animate-spin" aria-hidden="true" />
					{mode === 'login' ? m['login.button.signing_in']() : m['login.button.creating']()}
				{:else}
					{mode === 'login' ? m['login.button.signin']() : m['login.button.create_account']()}
				{/if}
			</button>
		</form>

		<p class="text-center text-sm text-[var(--color-text-secondary)]">
			{mode === 'login' ? m['login.no_account']() : m['login.has_account']()}
			<a href={mode === 'login' ? '/register' : '/login'} class="text-[var(--app-accent)] hover:underline">
				{mode === 'login' ? m['login.button.sign_up']() : m['login.button.signin']()}
			</a>
		</p>
	</div>

	{#if demoMode && mode === 'login'}
		<div class="fixed right-4 bottom-4 z-50 flex w-[calc(100vw-2rem)] max-w-sm flex-col items-end gap-3 sm:right-6 sm:bottom-6">
			{#if demoDrawerOpen}
				<section
					aria-label={m['login.demo.aria_details']()}
					class="w-full origin-bottom-right rounded-xl border border-[var(--app-border)] bg-[var(--color-bg-secondary)] p-4 text-sm text-[var(--color-text-secondary)] shadow-2xl"
				>
					<div class="mb-3 flex items-start justify-between gap-4">
						<div>
							<h2 class="font-medium text-[var(--color-text-primary)]">{m['login.demo.title']()}</h2>
							<p class="mt-1 text-xs">{m['login.demo.description']()}</p>
						</div>
						<button
							type="button"
							onclick={() => (demoDrawerOpen = false)}
							aria-label={m['login.demo.aria_close']()}
							class="rounded px-2 py-1 text-xs text-[var(--color-text-tertiary)] hover:bg-[var(--color-bg)] hover:text-[var(--color-text-primary)]"
						>
							{m['login.demo.close']()}
						</button>
					</div>

					<div class="max-h-[60vh] space-y-2 overflow-y-auto pr-1">
						{#each demoUsers as user}
							<button
								type="button"
								onclick={() => useDemoUser(user)}
								class="w-full rounded-lg border border-[var(--app-border)] bg-[var(--color-bg)] p-3 text-left transition hover:border-[var(--app-accent)] hover:bg-[var(--color-bg-secondary)]"
							>
								<div class="flex items-center justify-between gap-3">
									<p class="font-medium text-[var(--color-text-primary)]">{user.label}</p>
									<span class="text-xs text-[var(--app-accent)]">{m['login.demo.use']()}</span>
								</div>
								<dl class="mt-2 space-y-1 text-xs">
									<div class="flex justify-between gap-3">
										<dt>{m['login.email']()}</dt>
										<dd class="font-mono text-[var(--color-text-primary)]">{user.email}</dd>
									</div>
									<div class="flex justify-between gap-3">
										<dt>{m['login.password']()}</dt>
										<dd class="font-mono text-[var(--color-text-primary)]">{user.password}</dd>
									</div>
								</dl>
							</button>
						{/each}
					</div>
				</section>
			{/if}

			<button
				type="button"
				onclick={() => (demoDrawerOpen = !demoDrawerOpen)}
				aria-expanded={demoDrawerOpen}
				class="inline-flex items-center gap-2 rounded-full bg-[var(--app-accent)] px-4 py-2 text-sm font-medium text-[var(--app-accent-foreground)] shadow-lg transition hover:bg-[var(--app-accent-hover)]"
			>
				{#if demoDrawerOpen}
					<ChevronDown size={14} aria-hidden="true" />
				{:else}
					<ChevronUp size={14} aria-hidden="true" />
				{/if}
				{m['login.demo.users_button']()}
			</button>
		</div>
	{/if}
</div>

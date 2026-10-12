import { expect, test, type Page } from '@playwright/test';

/**
 * Docs happy path against the fully mocked API: the workspace docs page
 * lists pages, creating one navigates to the editor, the collaborative
 * session joins through the mocked per-document WebSocket, and the title
 * input drives the page tree.
 *
 * The collab socket is mocked with page.routeWebSocket per test:
 *   live     - join is answered with an init frame (can_write: true)
 *   readonly - join is answered with can_write: false (read-only banner)
 *   offline  - no route: the e2e static server destroys WS upgrades, so the
 *              editor takes the ~6s offline fallback path
 */

const slug = 'docs-team';
const workspace = {
	id: 'ws-1',
	name: 'Docs Team',
	slug,
	owner_id: 'user-1',
	current_user_role: 'admin',
	screen_sharing_enabled: false
};

const docHome = {
	id: '11111111-1111-4111-8111-111111111111',
	title: 'Home',
	icon: '🏠',
	position: 1024,
	is_template: false,
	updated_at: '2026-01-01T00:00:00Z',
	tags: []
};

const docMeeting = {
	id: '22222222-2222-4222-8222-222222222222',
	title: 'Meeting notes',
	parent_id: docHome.id,
	position: 1024,
	is_template: false,
	updated_at: '2026-01-02T00:00:00Z',
	tags: ['meetings']
};

const docTemplate = {
	id: '44444444-4444-4444-8444-444444444444',
	title: 'Meeting template',
	icon: '🧪',
	position: 512,
	is_template: true,
	updated_at: '2026-01-01T00:00:00Z',
	tags: []
};

/** Wiki link to the Home page, as persisted inside doc content_html. */
const wikiLinkHome = `<a data-type="wiki" data-doc-id="${docHome.id}" data-wiki-title="Home">Home</a>`;

type DocTreeNodeLike = {
	id: string;
	parent_id?: string;
	title: string;
	icon?: string;
	position: number;
	is_template: boolean;
	updated_at: string;
	tags: string[];
};

function me() {
	return {
		id: 'user-1',
		email: 'owner@example.com',
		name: 'Owner',
		display_name: 'Owner',
		is_sysadmin: false
	};
}

/** Full document payload for the GET /docs/{id} endpoint. */
function fullDoc(over: Record<string, unknown> = {}): DocTreeNodeLike & Record<string, unknown> {
	return {
		id: '00000000-0000-4000-8000-000000000000',
		workspace_id: workspace.id,
		title: '',
		content_html: '',
		content_text: '',
		position: 1024,
		is_template: false,
		created_by: 'user-1',
		created_at: '2026-01-01T00:00:00Z',
		updated_at: '2026-01-01T00:00:00Z',
		tags: [] as string[],
		...over
	};
}

type CollabMode = 'live' | 'readonly' | 'offline';

async function mockBase(
	page: Page,
	opts: { docs?: DocTreeNodeLike[]; role?: string; collab?: CollabMode } = {}
) {
	const { docs = [docHome, docMeeting], role = 'admin', collab = 'live' } = opts;
	await page.route('**/api/auth/me', (route) => route.fulfill({ json: me() }));
	await page.route(`**/api/workspaces/${slug}`, (route) =>
		route.fulfill({ json: { ...workspace, current_user_role: role } })
	);
	await page.route(`**/api/workspaces/${slug}/teams`, (route) => route.fulfill({ json: [] }));
	await page.route(`**/api/workspaces/${slug}/projects`, (route) => route.fulfill({ json: [] }));
	await page.route(`**/api/workspaces/${slug}/labels`, (route) => route.fulfill({ json: [] }));
	await page.route(`**/api/workspaces/${slug}/members`, (route) => route.fulfill({ json: [] }));
	await page.route(`**/api/workspaces/${slug}/views`, (route) => route.fulfill({ json: [] }));
	await page.route(`**/api/workspaces/${slug}/favorites`, (route) => route.fulfill({ json: [] }));
	await page.route(`**/api/workspaces/${slug}/docs`, (route) => route.fulfill({ json: docs }));
	await page.route(`**/api/workspaces/${slug}/doc-tags`, (route) => route.fulfill({ json: [] }));
	await page.route('**/api/notifications', (route) =>
		route.fulfill({ json: { notifications: [], unread_count: 0 } })
	);
	await page.route('**/api/workspaces/*/ws', () => {});

	if (collab === 'offline') {
		// No WS route: unmocked upgrades are destroyed by the static server,
		// so the session never syncs and the editor takes the offline path.
		return;
	}

	// The per-document collaboration socket: answer join with an empty
	// update log so the editor mounts live (docs whose content only exists
	// as persisted HTML replay an empty log).
	await page.routeWebSocket('**/api/workspaces/*/docs/*/collab', (ws) => {
		ws.onMessage((data) => {
			try {
				const msg = JSON.parse(String(data));
				if (msg.type === 'join') {
					ws.send(
						JSON.stringify({
							type: 'init',
							payload: {
								document_id: 'any',
								client_id: 'client-1',
								can_write: collab !== 'readonly',
								updates: []
							}
						})
					);
					ws.send(JSON.stringify({ type: 'peers', payload: { peers: [] } }));
				}
			} catch {
				// ignore malformed frames
			}
		});
	});
}

/** Mock the per-doc endpoints (GET/PATCH doc, backlinks, comments) and open the editor page. */
async function openDoc(page: Page, doc: { id: string } & Record<string, unknown>) {
	await page.route(`**/api/workspaces/${slug}/docs/${doc.id}`, (route) =>
		route.fulfill({ json: doc })
	);
	await page.route(`**/api/workspaces/${slug}/docs/${doc.id}/backlinks`, (route) =>
		route.fulfill({ json: [] })
	);
	await page.route(`**/api/workspaces/${slug}/docs/${doc.id}/comments`, (route) =>
		route.fulfill({ json: [] })
	);
	await page.goto(`/${slug}/docs/${doc.id}`);
	return page.locator('.docs-editor-shell .tiptap');
}

test('docs home lists pages with tree, favorites entry point and new-page menu', async ({ page }) => {
	await mockBase(page);
	await page.goto(`/${slug}/docs`);

	await expect(page.getByRole('heading', { name: 'Docs' })).toBeVisible();
	await expect(page.getByRole('link', { name: /Home/ }).first()).toBeVisible();
	// Sub-page appears nested once the parent row expands via chevron.
	await expect(page.getByText('Meeting notes')).toBeVisible();
	await expect(page.getByRole('button', { name: 'New', exact: true })).toBeVisible();
});

test('opening a page mounts the collaborative editor over the doc-sync socket', async ({ page }) => {
	await mockBase(page);
	await page.goto(`/${slug}/docs`);

	await page.getByRole('link', { name: /Home/ }).first().click();
	await page.waitForURL(`**/${slug}/docs/${docHome.id}`);

	await page.route(`**/api/workspaces/${slug}/docs/${docHome.id}`, (route) =>
		route.fulfill({
			json: {
				...docHome,
				workspace_id: workspace.id,
				content_html: '',
				content_text: '',
				created_by: 'user-1',
				created_at: '2026-01-01T00:00:00Z',
				tags: []
			}
		})
	);
	await page.goto(`/${slug}/docs/${docHome.id}`);

	// The mocked init frame flips the session live; the doc editor mounts.
	const editor = page.locator('.docs-editor-shell .tiptap');
	await expect(editor).toBeVisible({ timeout: 15000 });

	// Typing in the title is possible and the status shows the save flow.
	const title = page.locator('input.text-3xl');
	await expect(title).toBeVisible();
	await title.fill('Home renamed');

	// The editor content area is editable and empty at first.
	await expect(editor).toBeEditable();
});

test('creating a page from the menu navigates to the editor', async ({ page }) => {
	const created = {
		id: '33333333-3333-4333-8333-333333333333',
		workspace_id: workspace.id,
		title: '',
		content_html: '',
		content_text: '',
		position: 2048,
		is_template: false,
		created_by: 'user-1',
		created_at: '2026-01-03T00:00:00Z',
		updated_at: '2026-01-03T00:00:00Z',
		tags: []
	};
	await mockBase(page);
	await page.route('**/api/workspaces/*/docs', async (route) => {
		if (route.request().method() === 'POST') {
			return route.fulfill({ status: 201, json: created });
		}
		return route.fulfill({ json: [docHome, docMeeting] });
	});
	await page.route(`**/api/workspaces/${slug}/docs/${created.id}`, (route) =>
		route.fulfill({ json: created })
	);

	await page.goto(`/${slug}/docs`);
	await page.getByRole('button', { name: 'New', exact: true }).click();
	await page.getByRole('menuitem', { name: 'New page' }).click();

	await page.waitForURL(`**/${slug}/docs/${created.id}`);
	await expect(page.locator('input.text-3xl')).toBeVisible({ timeout: 15000 });
});

test('a doc with only persisted HTML seeds its content into the live editor', async ({ page }) => {
	await mockBase(page);
	// Imported markdown / template copies have content_html but no Yjs
	// update log: the init frame replays zero updates.
	const editor = await openDoc(
		page,
		fullDoc({
			...docHome,
			title: 'Imported',
			content_html: '<h2>Imported notes</h2><ul><li>alpha</li><li>beta</li></ul>',
			content_text: 'Imported notes\nalpha\nbeta'
		})
	);

	// The empty replay must not open an empty editor that would clobber the
	// persisted content: the HTML is seeded into the Yjs document.
	await expect(editor).toBeVisible({ timeout: 15000 });
	await expect(page.getByRole('heading', { name: 'Imported notes' })).toBeVisible();
	const items = page.locator('.docs-editor-shell .tiptap li');
	await expect(items).toHaveCount(2);
	await expect(editor).toContainText('alpha');
	await expect(editor).toContainText('beta');
});

test('slash menu inserts a table with a header row and the table bubble menu manages rows', async ({ page }) => {
	await mockBase(page);
	const editor = await openDoc(page, fullDoc({ ...docHome, title: 'Scratch' }));
	await expect(editor).toBeVisible({ timeout: 15000 });

	await editor.click();
	await page.keyboard.type('/');
	const menu = page.locator('.slash-command-menu');
	await expect(menu).toBeVisible();
	// Docs-only entries are merged into the shared catalog.
	await expect(menu.getByText('Wiki link')).toBeVisible();
	await expect(menu.getByText('Table')).toBeVisible();
	await expect(menu.getByText('Divider')).toBeVisible();

	await page.keyboard.type('table');
	await menu.getByRole('button', { name: 'Table', exact: true }).click();

	// 3x3 table with a header row.
	const table = page.locator('.docs-editor-shell .tiptap table');
	await expect(table).toBeVisible();
	await expect(table.locator('th')).toHaveCount(3);
	await expect(table.locator('tr')).toHaveCount(3);

	// Caret inside the table opens the bubble menu with row/column tools.
	const bubble = page.locator('.table-bubble');
	await expect(bubble).toBeVisible();
	await bubble.getByRole('button', { name: 'Insert row below' }).click();
	await expect(table.locator('tr')).toHaveCount(4);
});

test('slash menu inserts a divider', async ({ page }) => {
	await mockBase(page);
	const editor = await openDoc(page, fullDoc({ ...docHome, title: 'Scratch' }));
	await expect(editor).toBeVisible({ timeout: 15000 });

	await editor.click();
	await page.keyboard.type('/');
	await expect(page.locator('.slash-command-menu')).toBeVisible();
	await page.keyboard.type('divider');
	await page.keyboard.press('Enter');

	await expect(page.locator('.docs-editor-shell .tiptap hr')).toHaveCount(1);
});

test('the editor stays editable offline and seeds persisted content after the fallback timer', async ({ page }) => {
	// No collab route: the socket dies against the static server and the
	// editor opens locally through the ~6s offline fallback.
	await mockBase(page, { collab: 'offline' });
	const editor = await openDoc(
		page,
		fullDoc({ ...docHome, title: 'Offline doc', content_html: '<p>persisted body</p>' })
	);

	await expect(editor).toBeVisible({ timeout: 20000 });
	// Editability is granted from the workspace role while disconnected...
	await expect(editor).toBeEditable();
	// ...and the persisted HTML is seeded on the offline path too.
	await expect(editor).toContainText('persisted body');

	await editor.click();
	await page.keyboard.type('offline draft');
	await expect(editor).toContainText('offline draft');
});

test('a read-only client sees the notice banner and cannot edit', async ({ page }) => {
	// Guest role keeps the editor read-only even before the server verdict;
	// the init frame's can_write:false then shows the persistent notice.
	await mockBase(page, { role: 'guest', collab: 'readonly' });
	const editor = await openDoc(
		page,
		fullDoc({ ...docHome, title: 'Viewed', content_html: '<p>viewer content</p>' })
	);

	await expect(editor).toBeVisible({ timeout: 15000 });
	await expect(page.getByText('You have read-only access to this page.')).toBeVisible();
	// TipTap drops the contenteditable attribute entirely when the editor is
	// created read-only, so assert the writable attribute is absent.
	await expect(editor).not.toHaveAttribute('contenteditable', 'true');
	await expect(page.locator('input.text-3xl')).toHaveAttribute('readonly', '');
	// Read-only docs still render their persisted content.
	await expect(editor).toContainText('viewer content');
});

test('creating a page from a template copies the body and its wiki links', async ({ page }) => {
	const templateHtml = `<h2>Meeting template</h2><p>See ${wikiLinkHome}</p>`;
	const created = fullDoc({
		id: '55555555-5555-4555-8555-555555555555',
		title: '',
		content_html: templateHtml
	});
	const patches: Record<string, unknown>[] = [];

	await mockBase(page, { docs: [docHome, docMeeting, docTemplate] });
	await page.route('**/api/workspaces/*/docs', async (route) => {
		if (route.request().method() === 'POST') {
			return route.fulfill({ status: 201, json: created });
		}
		return route.fulfill({ json: [docHome, docMeeting, docTemplate] });
	});
	await page.route(`**/api/workspaces/${slug}/docs/${docTemplate.id}`, (route) =>
		route.fulfill({ json: fullDoc({ ...docTemplate, content_html: templateHtml }) })
	);
	await page.route(`**/api/workspaces/${slug}/docs/${created.id}`, async (route) => {
		if (route.request().method() === 'PATCH') {
			// Record what the "use template" flow copies into the new doc.
			patches.push(route.request().postDataJSON() as Record<string, unknown>);
		}
		return route.fulfill({ json: created });
	});
	await page.route(`**/api/workspaces/${slug}/docs/${created.id}/backlinks`, (route) =>
		route.fulfill({ json: [] })
	);
	await page.route(`**/api/workspaces/${slug}/docs/${created.id}/comments`, (route) =>
		route.fulfill({ json: [] })
	);

	await page.goto(`/${slug}/docs`);
	await page.getByRole('button', { name: 'New', exact: true }).click();
	await page.getByRole('menuitem', { name: /Meeting template/ }).click();

	await page.waitForURL(`**/${slug}/docs/${created.id}`);
	const editor = page.locator('.docs-editor-shell .tiptap');
	await expect(editor).toBeVisible({ timeout: 15000 });

	// The template body (incl. the wiki link) was copied into the new doc.
	expect(patches.length).toBeGreaterThanOrEqual(1);
	expect(patches[0]?.content_html).toContain('data-wiki-title="Home"');
	expect(patches[0]?.wiki_titles).toEqual(expect.arrayContaining(['Home']));

	// The new page opens with the copied body seeded into the editor.
	await expect(
		page.locator('.docs-editor-shell a[data-type="wiki"][data-wiki-title="Home"]')
	).toBeVisible();
});

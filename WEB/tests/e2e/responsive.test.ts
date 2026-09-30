import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';

// One test per indexable route: each gets its own timeout and runs in
// parallel, so a slow runner cannot fail the whole sweep, and a failure
// names its route. The list comes from the built sitemap (`npm run build`
// runs before these tests, and the preview server serves that build).
const sitemap = readFileSync(new URL('../../build/sitemap.xml', import.meta.url), 'utf8');
const routes = [...sitemap.matchAll(/<loc>([^<]+)<\/loc>/g)].map((match) => new URL(match[1]).pathname);

test.describe.configure({ mode: 'parallel' });

test('the sitemap lists every indexable route', () => {
	expect(routes).toHaveLength(32);
});

for (const route of routes) {
	test(`${route} fits a 320px viewport`, async ({ page }) => {
		await page.goto(route, { waitUntil: 'networkidle' });
		const layout = await page.evaluate(() => {
			const viewportWidth = document.documentElement.clientWidth;
			const scrollWidth = Math.max(document.documentElement.scrollWidth, document.body.scrollWidth);
			const offenders = [...document.body.querySelectorAll<HTMLElement>('*')]
				.map((element) => ({ element, rect: element.getBoundingClientRect() }))
				.filter(({ rect }) => rect.right > viewportWidth + 0.5 || rect.left < -0.5)
				.slice(0, 5)
				.map(({ element, rect }) => ({
					tag: element.tagName.toLowerCase(),
					className: element.className,
					left: Math.round(rect.left),
					right: Math.round(rect.right)
				}));
			return { overflow: scrollWidth - viewportWidth, offenders };
		});

		expect(layout.overflow, JSON.stringify(layout.offenders)).toBeLessThanOrEqual(0);
	});
}

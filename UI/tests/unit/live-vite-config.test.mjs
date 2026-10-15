import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { copyFile, mkdir, mkdtemp, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const uiDir = fileURLToPath(new URL('../..', import.meta.url));
const launcher = fileURLToPath(new URL('../../../scripts/check-screen-share-live.mjs', import.meta.url));

test('the shipped live launcher retains all Vite plugins and isolates both environment loads', async () => {
	const fixture = await mkdtemp(path.join(os.tmpdir(), 'strifira-live-vite-config-test-'));
	try {
		const fixtureUI = path.join(fixture, 'UI');
		const isolatedEnv = path.join(fixture, 'isolated');
		await mkdir(fixtureUI);
		await mkdir(isolatedEnv);
		await symlink(path.join(uiDir, 'node_modules'), path.join(fixtureUI, 'node_modules'), 'dir');
		await copyFile(path.join(uiDir, 'vite.config.ts'), path.join(fixtureUI, 'vite.config.ts'));
		await copyFile(path.join(uiDir, 'svelte.config.js'), path.join(fixtureUI, 'svelte.config.js'));
		await writeFile(path.join(fixtureUI, 'package.json'), '{"type":"module"}\n');
		await writeFile(
			path.join(fixture, '.env'),
			'STRIFIRA_DEV_API_URL=http://repository-env.invalid:43111\nSTRIFIRA_DEV_UI_PORT=43112\n'
		);
		await writeFile(
			path.join(fixture, '.env.production'),
			'STRIFIRA_DEV_API_URL=http://repository-production-env.invalid:43211\nSTRIFIRA_DEV_UI_PORT=43212\n'
		);
		const source = `
import { loadConfigFromFile } from 'vite';
import { writeIsolatedViteConfig } from ${JSON.stringify(pathToFileURL(launcher).href)};
const base = ${JSON.stringify(path.join(fixtureUI, 'vite.config.ts'))};
const generated = ${JSON.stringify(path.join(isolatedEnv, 'vite-live.config.mjs'))};
const envDir = ${JSON.stringify(isolatedEnv)};
await writeIsolatedViteConfig(generated, base, envDir);
async function plugins(options) {
  const names = [];
  for (const option of await Promise.all(options)) {
    if (Array.isArray(option)) names.push(...await plugins(option));
    else if (option) names.push(option.name);
  }
  return names;
}
async function load(file) {
  const loaded = await loadConfigFromFile({ command: 'build', mode: 'production' }, file, process.cwd());
  if (!loaded) throw new Error('Config failed to load');
  return {
    envDir: loaded.config.envDir,
    api: loaded.config.server.proxy['/api'].target,
    uiPort: loaded.config.server.port,
    plugins: await plugins(loaded.config.plugins)
  };
}
console.log(JSON.stringify({ isolated: await load(generated), normal: await load(base) }));
`;
		const result = spawnSync(process.execPath, ['--input-type=module', '-e', source], {
			cwd: fixtureUI,
			env: { PATH: process.env.PATH },
			encoding: 'utf8',
			timeout: 20_000
		});
		assert.equal(result.error, undefined);
		assert.equal(result.status, 0, result.stderr);
		const configs = JSON.parse(result.stdout.trim().split('\n').at(-1));
		assert.equal(configs.isolated.envDir, isolatedEnv);
		assert.equal(configs.isolated.api, 'http://localhost:8080');
		assert.equal(configs.isolated.uiPort, 5173);
		assert.deepEqual(configs.isolated.plugins, configs.normal.plugins);
		assert.ok(configs.isolated.plugins.some((name) => name.includes('sveltekit')));
		assert.ok(configs.isolated.plugins.some((name) => name.includes('svelte')));
		assert.ok(configs.isolated.plugins.some((name) => name.includes('paraglide')));
		assert.ok(configs.isolated.plugins.some((name) => name.includes('tailwind')));
		assert.equal(configs.normal.envDir, '..');
		assert.equal(configs.normal.api, 'http://repository-production-env.invalid:43211');
		assert.equal(configs.normal.uiPort, 43212);
	} finally {
		await rm(fixture, { recursive: true, force: true });
	}
});

#!/usr/bin/env node
// Fails when any locale in messages/ is missing a key present in the source
// locale (en.json), carries a key the source locale does not have, or uses a
// {placeholder} the source message does not define. (Dropping a placeholder is
// allowed: languages without plurals omit {s}/{plural}.) Run with `npm run check:locales`.
import { readFileSync, readdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const messagesDir = join(dirname(fileURLToPath(import.meta.url)), '..', 'messages');
const sourceLocale = 'en';

function load(locale) {
	const raw = JSON.parse(readFileSync(join(messagesDir, `${locale}.json`), 'utf8'));
	delete raw.$schema;
	return raw;
}

function placeholders(value) {
	return new Set(typeof value === 'string' ? [...value.matchAll(/\{([A-Za-z0-9_]+)\}/g)].map((m) => m[1]) : []);
}

const source = load(sourceLocale);
const sourceKeys = Object.keys(source);
const locales = readdirSync(messagesDir)
	.filter((file) => file.endsWith('.json'))
	.map((file) => file.slice(0, -'.json'.length))
	.filter((locale) => locale !== sourceLocale)
	.sort();

let failed = false;
for (const locale of locales) {
	const messages = load(locale);
	const missing = sourceKeys.filter((key) => !(key in messages));
	const extra = Object.keys(messages).filter((key) => !(key in source));
	const badPlaceholders = sourceKeys.filter((key) => {
		if (!(key in messages)) return false;
		const known = placeholders(source[key]);
		return [...placeholders(messages[key])].some((name) => !known.has(name));
	});
	if (missing.length || extra.length || badPlaceholders.length) {
		failed = true;
		console.error(`${locale}.json:`);
		for (const key of missing) console.error(`  missing key: ${key}`);
		for (const key of extra) console.error(`  extra key: ${key}`);
		for (const key of badPlaceholders) console.error(`  placeholder not defined in ${sourceLocale}.json: ${key}`);
	}
}

if (failed) {
	console.error(`Locale check failed: every locale must have exactly the keys of ${sourceLocale}.json.`);
	process.exit(1);
}
console.log(`Locale check passed: ${locales.join(', ')} match ${sourceKeys.length} keys of ${sourceLocale}.json.`);

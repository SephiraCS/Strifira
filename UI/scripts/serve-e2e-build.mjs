import { createReadStream } from 'node:fs';
import { stat } from 'node:fs/promises';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

// Exercise the shipped static adapter output, including its runtime env.js.
// Vite preview uses the SvelteKit server and inlines environment settings,
// which bypasses the browser tests' runtime configuration mocks.
const buildDir = fileURLToPath(new URL('../build/', import.meta.url));
const port = Number(process.argv[2] ?? 4174);
const mime = {
	'.html': 'text/html; charset=utf-8',
	'.js': 'application/javascript; charset=utf-8',
	'.css': 'text/css; charset=utf-8',
	'.json': 'application/json',
	'.svg': 'image/svg+xml',
	'.png': 'image/png',
	'.webp': 'image/webp',
	'.woff2': 'font/woff2',
	'.wasm': 'application/wasm'
};

await stat(path.join(buildDir, 'index.html'));
const server = http.createServer(async (request, response) => {
	try {
		const pathname = decodeURIComponent(new URL(request.url, 'http://localhost').pathname);
		let filename = path.resolve(buildDir, `.${pathname}`);
		if (filename !== path.resolve(buildDir) && !filename.startsWith(buildDir)) {
			response.writeHead(403).end();
			return;
		}
		const file = await stat(filename).catch(() => null);
		if (!file?.isFile()) {
			// API and asset misses must fail instead of returning the SPA shell.
			if (pathname.startsWith('/api/') || pathname.startsWith('/_app/') || path.extname(pathname)) {
				response.writeHead(404).end();
				return;
			}
			filename = path.join(buildDir, 'index.html');
		}
		response.writeHead(200, {
			'Content-Type': mime[path.extname(filename)] ?? 'application/octet-stream',
			'Cache-Control': 'no-store'
		});
		if (request.method === 'HEAD') response.end();
		else
			createReadStream(filename)
				.on('error', () => response.destroy())
				.pipe(response);
	} catch {
		response.writeHead(400).end();
	}
});

// Suites mock their APIs and WebSockets; fail unmocked connections promptly.
server.on('upgrade', (_request, socket) => socket.destroy());
server.listen(port, '127.0.0.1');

// strifira service worker: makes the app installable and gives navigations an
// offline fallback. Deliberately no precaching — the shell and its hashed
// assets must never go stale across deploys; every real request still goes
// to the network.
const OFFLINE_HTML = `<!doctype html>
<html lang="en">
<head>
	<meta charset="utf-8" />
	<meta name="viewport" content="width=device-width, initial-scale=1" />
	<title>Strifira — offline</title>
	<style>
		html { background: #111111; color: #f5f5f5; font-family: system-ui, sans-serif; }
		body { min-height: 100vh; display: flex; align-items: center; justify-content: center; margin: 0; }
		p { opacity: 0.8; }
	</style>
</head>
<body>
	<p>Strifira is offline. Reconnect and reload.</p>
</body>
</html>`;

self.addEventListener('install', () => {
	self.skipWaiting();
});

self.addEventListener('activate', (event) => {
	event.waitUntil(self.clients.claim());
});

self.addEventListener('fetch', (event) => {
	// Only HTML navigations get the offline fallback; everything else
	// (API, WebSocket upgrades, hashed assets) is untouched network traffic.
	if (event.request.method !== 'GET' || event.request.mode !== 'navigate') return;
	event.respondWith(
		fetch(event.request).catch(
			() =>
				new Response(OFFLINE_HTML, {
					headers: { 'Content-Type': 'text/html; charset=utf-8' }
				})
		)
	);
});

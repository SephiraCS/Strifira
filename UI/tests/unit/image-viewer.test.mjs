import assert from 'node:assert/strict';
import test from 'node:test';
import {
	MAX_SCALE,
	MIN_SCALE,
	anchoredOffset,
	clampScale,
	extensionForContentType,
	filenameFromDisposition,
	filenameFromUrl,
	formatZoomPercent,
	imageFilename,
	sanitizeFilename,
	zoomScale
} from '../../src/lib/components/shared/image-viewer/image-viewer.ts';

test('clamps the zoom factor to the supported range', () => {
	assert.equal(MIN_SCALE, 0.25);
	assert.equal(MAX_SCALE, 8);
	assert.equal(clampScale(0.1), MIN_SCALE);
	assert.equal(clampScale(100), MAX_SCALE);
	assert.equal(clampScale(1.5), 1.5);
	assert.equal(clampScale(Number.NaN), 1);
	assert.equal(clampScale(Number.POSITIVE_INFINITY), 1);
});

test('applies relative zoom steps without leaving the range', () => {
	assert.equal(zoomScale(1, 0.25), 1.25);
	assert.equal(zoomScale(MIN_SCALE, -0.25), MIN_SCALE);
	assert.equal(zoomScale(MAX_SCALE, 0.25), MAX_SCALE);
	assert.equal(zoomScale(Number.NaN, 0.25), 1.25);
});

test('formats zoom factors as percentages', () => {
	assert.equal(formatZoomPercent(1), '100%');
	assert.equal(formatZoomPercent(1.25), '125%');
	assert.equal(formatZoomPercent(0.1), '25%');
});

test('keeps the pointer anchored while zooming', () => {
	const result = anchoredOffset({
		offsetX: 0,
		offsetY: 0,
		scale: 1,
		nextScale: 2,
		pointerX: 600,
		pointerY: 400,
		centerX: 500,
		centerY: 400
	});

	assert.deepEqual(result, { x: -100, y: 0 });

	// The image point under the pointer must stay put.
	const before = (600 - 500 - 0) / 1;
	const after = (600 - 500 - result.x) / 2;
	assert.equal(before, after);
});

test('ignores invalid anchors instead of producing NaN offsets', () => {
	const result = anchoredOffset({
		offsetX: 10,
		offsetY: 20,
		scale: 0,
		nextScale: 2,
		pointerX: 0,
		pointerY: 0,
		centerX: 0,
		centerY: 0
	});
	assert.deepEqual(result, { x: 10, y: 20 });
});

test('maps image content types to extensions', () => {
	assert.equal(extensionForContentType('image/jpeg'), 'jpg');
	assert.equal(extensionForContentType('image/jpeg; charset=binary'), 'jpg');
	assert.equal(extensionForContentType('image/svg+xml'), 'svg');
	assert.equal(extensionForContentType('image/webp'), 'webp');
	assert.equal(extensionForContentType('application/pdf'), null);
	assert.equal(extensionForContentType(null), null);
	assert.equal(extensionForContentType(''), null);
});

test('parses Content-Disposition filenames', () => {
	assert.equal(filenameFromDisposition('attachment; filename="My Screenshot.png"'), 'My Screenshot.png');
	assert.equal(filenameFromDisposition('attachment; filename=diagram.svg'), 'diagram.svg');
	assert.equal(filenameFromDisposition("attachment; filename*=UTF-8''Na%C3%AFve%20img.png"), 'Naïve img.png');
	assert.equal(filenameFromDisposition('inline'), null);
	assert.equal(filenameFromDisposition(null), null);
});

test('sanitizes unsafe filename characters', () => {
	assert.equal(sanitizeFilename('../../etc/passwd').includes('/'), false);
	assert.equal(sanitizeFilename('..\\..\\windows\\system32').includes('\\'), false);
	assert.equal(sanitizeFilename('  spaced.png  '), 'spaced.png');
	assert.equal(sanitizeFilename('bad\u0000name.png'), 'badname.png');
	assert.equal(sanitizeFilename(''), '');
});

test('derives filenames from URLs only when they carry an extension', () => {
	assert.equal(filenameFromUrl('/api/workspaces/acme/assets/2f1c9b5e-0000-0000-0000-000000000000'), null);
	assert.equal(filenameFromUrl('/files/photo%20(1).PNG'), 'photo (1).PNG');
	assert.equal(filenameFromUrl('https://example.com/a/b/pic.jpeg?width=100'), 'pic.jpeg');
	assert.equal(filenameFromUrl(undefined), null);
});

test('resolves a download filename with a sensible preference order', () => {
	assert.equal(imageFilename({ explicit: 'report.pdf', src: '/api/workspaces/acme/assets/abc' }), 'report.pdf');
	assert.equal(imageFilename({ explicit: 'report', contentType: 'image/png' }), 'report.png');
	assert.equal(imageFilename({ disposition: 'attachment; filename="shot.jpg"' }), 'shot.jpg');
	assert.equal(imageFilename({ src: '/files/diagram.svg' }), 'diagram.svg');
	assert.equal(imageFilename({ src: '/api/workspaces/acme/assets/abc', alt: 'screenshot.png' }), 'screenshot.png');
	assert.equal(imageFilename({ src: '/api/workspaces/acme/assets/abc', contentType: 'image/webp' }), 'image.webp');
	assert.equal(imageFilename({}), 'image.png');
});

import assert from 'node:assert/strict';
import test from 'node:test';
import {
	SHARE_ZOOM_CAP,
	SHARE_ZOOM_STEP,
	autoZoomScale,
	clampPan,
	panBounds
} from '../../src/lib/features/shares/share-zoom.ts';

test('auto zoom fills the stage width only when the video is narrower', () => {
	// 16:9 video in a wider stage: zoom to fill the width (cap applies).
	assert.ok(Math.abs(autoZoomScale(21 / 9, 16 / 9) - 21 / 16) < 1e-9);
	// A wider video already fills the width: no zoom.
	assert.equal(autoZoomScale(4 / 3, 16 / 9), 1);
	// Matching aspects: no zoom.
	assert.equal(autoZoomScale(16 / 9, 16 / 9), 1);
});

test('auto zoom is capped and rejects invalid input', () => {
	assert.equal(autoZoomScale(10, 0.1), SHARE_ZOOM_CAP);
	assert.equal(SHARE_ZOOM_CAP, 2);
	assert.equal(autoZoomScale(Number.NaN, 16 / 9), 1);
	assert.equal(autoZoomScale(16 / 9, 0), 1);
	assert.equal(autoZoomScale(-2, 16 / 9), 1);
});

test('pan bounds are zero until the picture actually overflows', () => {
	// At fit (scale 1) nothing may pan.
	assert.deepEqual(panBounds(1000, 500, 16 / 9, 1), { maxX: 0, maxY: 0 });
	// Invalid stages never allow panning.
	assert.deepEqual(panBounds(0, 500, 16 / 9, 2), { maxX: 0, maxY: 0 });
	assert.deepEqual(panBounds(1000, 500, Number.NaN, 2), { maxX: 0, maxY: 0 });
});

test('pan bounds follow the letterboxed content size', () => {
	// 16:9 video (narrower than the 2:1 stage) fills the height: content is
	// 888.9x500, so at 2x both axes overflow.
	const fit = panBounds(1000, 500, 16 / 9, 2);
	assert.ok(Math.abs(fit.maxX - ((500 * 16) / 9 * 2 - 1000) / 2) < 1e-9);
	assert.ok(Math.abs(fit.maxY - 250) < 1e-9);

	// A wider-than-stage video (21:9) fills the width: content is 1000x428.6.
	// At a modest 1.1x only the horizontal axis overflows.
	const modest = panBounds(1000, 500, 21 / 9, 1.1);
	assert.ok(Math.abs(modest.maxX - 50) < 1e-9);
	assert.equal(modest.maxY, 0);

	// At 2x both axes overflow.
	const wide = panBounds(1000, 500, 21 / 9, 2);
	assert.ok(Math.abs(wide.maxX - 500) < 1e-9);
	assert.ok(Math.abs(wide.maxY - ((1000 / (21 / 9)) * 2 - 500) / 2) < 1e-9);
});

test('clampPan keeps the offset inside the bounds and ignores garbage', () => {
	assert.deepEqual(clampPan(500, -500, { maxX: 100, maxY: 50 }), { x: 100, y: -50 });
	assert.deepEqual(clampPan(10, 10, { maxX: 0, maxY: 0 }), { x: 0, y: 0 });
	assert.deepEqual(clampPan(Number.NaN, Number.POSITIVE_INFINITY, { maxX: 5, maxY: 5 }), { x: 0, y: 5 });
});

test('the zoom step stays small enough for fine control', () => {
	assert.equal(SHARE_ZOOM_STEP, 0.25);
});

import assert from 'node:assert/strict';
import test from 'node:test';
import {
	clampCropRect,
	commentBodyForAsset,
	escapeHtmlAttribute,
	normalizeRect,
	planCapture,
	scaleCropRect,
	screenshotFilename
} from '../../src/lib/features/screenshot/geometry.ts';
import { MAX_CANVAS_AREA, MAX_CANVAS_SIDE } from '../../src/lib/features/screenshot/geometry.ts';

const GEOMETRY = { viewportWidth: 1000, viewportHeight: 800, contentWidth: 1000, contentHeight: 800 };

test('plans a single chunk for content that fits one viewport', () => {
	const plan = planCapture(GEOMETRY, 1);
	assert.equal(plan.chunks.length, 1);
	assert.deepEqual(plan.chunks[0], {
		scrollX: 0,
		scrollY: 0,
		contentX: 0,
		contentY: 0,
		contentWidth: 1000,
		contentHeight: 800
	});
	assert.equal(plan.canvasWidth, 1000);
	assert.equal(plan.canvasHeight, 800);
});

test('steps viewport-height chunks and clamps the final partial chunk', () => {
	const plan = planCapture(
		{ viewportWidth: 1000, viewportHeight: 800, contentWidth: 1000, contentHeight: 2100 },
		1
	);
	assert.equal(plan.chunks.length, 3);
	assert.deepEqual(plan.chunks[1], {
		scrollX: 0,
		scrollY: 800,
		contentX: 0,
		contentY: 800,
		contentWidth: 1000,
		contentHeight: 800
	});
	// 1600 is beyond the reachable scrollTop (2100 - 800 = 1300); the chunk
	// keeps its content coordinates so the stitch lands at the right place.
	assert.equal(plan.chunks[2].scrollY, 1300);
	assert.equal(plan.chunks[2].contentY, 1600);
	assert.equal(plan.chunks[2].contentHeight, 500);
});

test('chunks horizontally overflowing content in both axes', () => {
	const plan = planCapture(
		{ viewportWidth: 1000, viewportHeight: 800, contentWidth: 2500, contentHeight: 800 },
		1
	);
	assert.equal(plan.chunks.length, 3);
	assert.equal(plan.chunks[1].contentX, 1000);
	assert.equal(plan.chunks[1].scrollX, 1000);
	assert.equal(plan.chunks[2].contentX, 2000);
	assert.equal(plan.chunks[2].contentWidth, 500);
	assert.equal(plan.chunks[2].scrollX, 1500);
});

test('halves the scale until the canvas respects the side and area caps', () => {
	const huge = {
		viewportWidth: 1600,
		viewportHeight: 900,
		contentWidth: 1600,
		contentHeight: 60000
	};
	const plan = planCapture(huge, 2);
	assert.ok(plan.canvasHeight <= MAX_CANVAS_SIDE, 'side cap');
	assert.ok(plan.canvasWidth * plan.canvasHeight <= MAX_CANVAS_AREA, 'area cap');
	assert.ok(plan.scale < 2, 'scale had to shrink');
	// Chunks stay in content coordinates regardless of the output scale.
	assert.ok(plan.chunks.length > 1);
});

test('floors the scale at 0.25 instead of looping forever', () => {
	const plan = planCapture(
		{ viewportWidth: 1000, viewportHeight: 800, contentWidth: 1000, contentHeight: 400000 },
		2
	);
	assert.equal(plan.scale, 0.25);
});

test('normalizes an inverted drag into a positive rect', () => {
	const rect = normalizeRect({ x: 200, y: 300 }, { x: 100, y: 120 });
	assert.deepEqual(rect, { x: 100, y: 120, width: 100, height: 180 });
});

test('clamps crops to the image bounds and rejects stray clicks', () => {
	assert.deepEqual(clampCropRect({ x: -10, y: -10, width: 500, height: 500 }, 200, 100, 1), {
		x: 0,
		y: 0,
		width: 200,
		height: 100
	});
	assert.deepEqual(clampCropRect({ x: 50, y: 50, width: 400, height: 400 }, 200, 100, 1), {
		x: 50,
		y: 50,
		width: 150,
		height: 50
	});
	assert.equal(clampCropRect({ x: 10, y: 10, width: 3, height: 3 }, 200, 100, 8), null);
});

test('scales a crop rect from display pixels to natural pixels', () => {
	const rect = scaleCropRect({ x: 10, y: 20, width: 30, height: 40 }, 2.5);
	assert.deepEqual(rect, { x: 25, y: 50, width: 75, height: 100 });
});

test('builds a sortable screenshot filename', () => {
	assert.equal(screenshotFilename(new Date(2026, 9, 27, 9, 5, 3)), 'screenshot-20261027-090503.png');
});

test('escapes HTML attribute values', () => {
	assert.equal(escapeHtmlAttribute('a"b<c>&d'), 'a&quot;b&lt;c&gt;&amp;d');
});

test('embeds an uploaded asset in a comment body like the editor does', () => {
	assert.equal(
		commentBodyForAsset('/api/workspaces/acme/assets/abc', 'shot "1".png'),
		'<p><img src="/api/workspaces/acme/assets/abc" alt="shot &quot;1&quot;.png"></p>'
	);
});

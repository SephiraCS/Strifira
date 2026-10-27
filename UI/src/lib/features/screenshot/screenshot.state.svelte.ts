/**
 * Screenshot capture state: a module-scope singleton shared by every top-bar
 * CaptureButton and the workspace-level CaptureDialog.
 *
 * Stage flow: idle —(start: getDisplayMedia / page scroll-stitch)→ preview →
 * (optional crop round-trip) → destination → submit (upload + comment /
 * files / download) → idle. A cancelled capture never opens the dialog.
 */
import { browser } from '$app/environment';
import { page } from '$app/state';
import { m } from '$lib/paraglide/messages.js';
import { appToast } from '$lib/features/toast/toast';
import { api } from '$lib/api/client';
import { createComment } from '$lib/api/issues';
import { commentBodyForAsset, screenshotFilename, type CropRect } from './geometry';
import {
	classifyCaptureError,
	displayCaptureSupported,
	grabDisplayFrame,
	type CaptureFailure
} from './screen.capture';
import { captureFullPage } from './page.capture';
import { cropBlob, OversizedCaptureError } from './video-frame';

export type CaptureMode = 'screen' | 'window' | 'page' | 'area';
export type CaptureStage = 'idle' | 'preview' | 'crop' | 'destination';
export type CaptureDestination = 'current-issue' | 'files' | 'download';

interface CapturedImage {
	url: string;
	blob: Blob;
	width: number;
	height: number;
}

interface UploadResult {
	url: string;
	filename: string;
	content_type?: string;
}

type UploadApiResponse = {
	data?: { url?: string; filename?: string; content_type?: string };
	url?: string;
	filename?: string;
	content_type?: string;
};

class ScreenshotCapture {
	stage = $state<CaptureStage>('idle');
	busy = $state(false);
	submitting = $state(false);
	image = $state<CapturedImage | null>(null);
	mode = $state<CaptureMode | null>(null);
	supported = $state(false);

	constructor() {
		this.supported = browser && displayCaptureSupported();
	}

	get busyOrSubmitting(): boolean {
		return this.busy || this.submitting;
	}

	/** Must be called from a user gesture: Chromium rejects getDisplayMedia otherwise. */
	async start(mode: CaptureMode): Promise<void> {
		if (!this.supported || this.busyOrSubmitting) return;
		this.mode = mode;
		this.busy = true;
		try {
			const frame =
				mode === 'page'
					? await captureFullPage()
					: await grabDisplayFrame(
							mode === 'screen' ? 'monitor' : mode === 'window' ? 'window' : undefined
						);
			this.setFrame(frame);
			if (mode === 'area') this.stage = 'crop';
		} catch (err) {
			this.handleCaptureError(err);
		} finally {
			this.busy = false;
		}
	}

	/** Re-runs the last capture mode (button click keeps the gesture alive). */
	recapture(): void {
		const mode = this.mode;
		this.dismiss();
		if (mode) void this.start(mode);
	}

	openCrop(): void {
		if (this.image) this.stage = 'crop';
	}

	cancelCrop(): void {
		if (this.image) this.stage = 'preview';
		else this.dismiss();
	}

	async applyCrop(rect: CropRect): Promise<void> {
		if (!this.image || this.busy) return;
		this.busy = true;
		try {
			const frame = await cropBlob(this.image.blob, rect);
			this.setFrame(frame, true);
			this.stage = 'preview';
		} catch (err) {
			appToast.apiError(err, m['screenshot.capture_failed']());
		} finally {
			this.busy = false;
		}
	}

	continueToDestination(): void {
		if (this.image) this.stage = 'destination';
	}

	backToPreview(): void {
		if (this.image) this.stage = 'preview';
	}

	dismiss(): void {
		this.clearImage();
		this.stage = 'idle';
	}

	/** The issue page the capture was started from, when there is one. */
	currentIssue(): { slug: string; identifier: string } | null {
		if (page.route?.id !== '/[workspaceSlug]/issue/[identifier]') return null;
		const { workspaceSlug, identifier } = page.params;
		if (!workspaceSlug || !identifier) return null;
		return { slug: workspaceSlug, identifier };
	}

	async submit(destination: CaptureDestination, targetIdentifier?: string): Promise<void> {
		const image = this.image;
		if (!image || this.busyOrSubmitting) return;
		if (destination === 'download') {
			this.download(image);
			return;
		}
		const slug = this.currentIssue()?.slug ?? page.params?.workspaceSlug;
		if (!slug) return;
		this.submitting = true;
		try {
			const filename = screenshotFilename();
			const uploaded = await this.upload(slug, image.blob, filename);
			if (destination === 'files') {
				appToast.success(m['screenshot.saved_files']());
				this.dismiss();
			} else {
				const identifier = targetIdentifier ?? this.currentIssue()?.identifier;
				if (!identifier) return;
				await createComment(
					slug,
					identifier,
					commentBodyForAsset(uploaded.url, uploaded.filename ?? filename)
				);
				appToast.success(m['screenshot.comment_added']({ identifier }));
				this.dismiss();
			}
		} catch (err) {
			if (err instanceof OversizedCaptureError) appToast.error(m['screenshot.oversized']());
			else appToast.apiError(err, m['screenshot.upload_failed']());
		} finally {
			this.submitting = false;
		}
	}

	download(image: CapturedImage = this.image!): void {
		if (!image) return;
		const anchor = document.createElement('a');
		anchor.href = image.url;
		anchor.download = screenshotFilename();
		document.body.appendChild(anchor);
		anchor.click();
		anchor.remove();
		appToast.success(m['screenshot.downloaded']());
		// The download reads the blob URL asynchronously; keep it alive briefly.
		setTimeout(() => this.dismiss(), 1000);
	}

	private setFrame(frame: { blob: Blob; width: number; height: number }, cropped = false): void {
		this.clearImage();
		this.image = {
			url: URL.createObjectURL(frame.blob),
			blob: frame.blob,
			width: frame.width,
			height: frame.height
		};
		if (!cropped) this.stage = 'preview';
	}

	private clearImage(): void {
		if (this.image) URL.revokeObjectURL(this.image.url);
		this.image = null;
	}

	private async upload(
		slug: string,
		blob: Blob,
		filename: string
	): Promise<UploadResult> {
		const form = new FormData();
		form.append('file', new File([blob], filename, { type: 'image/png' }));
		// Same endpoint and api client as every other upload, so an expired
		// access token is refreshed and retried like elsewhere in the app.
		const data = await api.postForm<UploadApiResponse>(`/api/workspaces/${slug}/upload`, form);
		const result = data?.data ?? data;
		if (!result?.url) throw new Error(m['screenshot.upload_failed']());
		return result as UploadResult;
	}

	private handleCaptureError(err: unknown): void {
		const kind: CaptureFailure = classifyCaptureError(err);
		if (kind === 'cancelled') return;
		if (kind === 'unavailable') appToast.error(m['screenshot.surface_unavailable']());
		else if (err instanceof OversizedCaptureError) appToast.error(m['screenshot.oversized']());
		else appToast.error(m['screenshot.capture_failed']());
	}
}

export const screenshotCapture = new ScreenshotCapture();

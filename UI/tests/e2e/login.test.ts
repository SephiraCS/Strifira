import { test, expect } from '@playwright/test';

test('login page loads', async ({ page }) => {
	await page.goto('/login');
	await expect(page.getByAltText('Ku logo')).toBeVisible();
	await expect(page.getByText('Ku')).toBeVisible();
	await expect(page.getByText('Sign in to your account')).toBeVisible();
});

test('login form has required fields', async ({ page }) => {
	await page.goto('/login');
	await expect(page.getByLabel('Email')).toBeVisible();
	await expect(page.getByRole('textbox', { name: 'Password', exact: true })).toBeVisible();
});

/**
 * Content registry for comparison pages.
 * Each entry maps to /compare/[slug].
 */
import type { ContentRegistry } from './routes';

export const compare: ContentRegistry = {
	'strifira-vs-linear': {
		slug: 'strifira-vs-linear',
		title: 'Strifira vs Linear — Comparison',
		description:
			'Compare Strifira and Linear by hosting model, license, issue workflow, planning, integrations, product maturity, and software cost.',
		heading: 'Strifira vs Linear',
		intro:
			'Both products emphasize fast, keyboard-oriented issue tracking. The main decision is not superficial feature parity: Linear is a mature hosted product, while Strifira is an early self-hosted project with complete source access and no software license fee.',
		sections: [
			{
				heading: 'Hosting and source access',
				body: 'The products use different delivery models.',
				list: [
					'Linear is a proprietary hosted service and does not offer a self-hosted edition.',
					'Strifira is self-hosted from one public Apache 2.0 repository. There is no hosted Strifira service.'
				]
			},
			{
				heading: 'Core issue tracking',
				body: 'Strifira covers a smaller product surface but differs on issue ownership.',
				list: [
					'Linear provides a mature issue workflow, command menu, filters and keyboard shortcuts.',
					'Strifira provides multiple assignees per issue, sub-issues, relations, labels, comments, saved views and team-specific statuses.'
				]
			},
			{
				heading: 'Planning: cycles and projects',
				body: 'Both products support time-boxed work and project grouping, but Linear’s planning layer is broader.',
				list: [
					'Linear: cycles, projects, roadmaps, and initiatives for grouping work across teams.',
					'Strifira has cycles with burndown and velocity charts, projects with progress and a Gantt view, and workspace/team analytics with configurable insights. It has no initiatives layer.'
				]
			},
			{
				heading: 'Integrations and automation',
				body: 'Check required integrations before choosing either product.',
				list: [
					'Linear: documents integrations for GitHub, GitLab, Slack, its API, and enterprise identity features.',
					'Strifira integrates with GitHub and generic webhooks. Its development branch includes an unreleased opt-in Dev Machines subsystem for agentic coding runs. It does not currently provide GitLab, Slack, SSO, SCIM or LDAP integrations.'
				]
			},
			{
				heading: 'Cost model',
				body: 'Published prices and plan limits can change; the linked pricing page is authoritative.',
				list: [
					'Linear offers a limited free plan and paid per-user plans. Hosting and product operations are included in that service.',
					'Strifira charges no software license fee. You pay for infrastructure and operate backups, monitoring and updates.'
				]
			},
			{
				heading: 'How to choose',
				body: 'Choose based on operating model and required maturity, not on headline similarity.',
				list: [
					'Choose Linear when a managed service, broader integrations and a mature product are more important than self-hosting.',
					'Choose Strifira when self-hosting, multiple assignees, source access and no software license fee are hard requirements—and the current MVP covers your workflow.'
				]
			},
			{
				heading: 'Sources',
				body: 'Review the first-party sources for the latest product details.',
				links: [
					{ label: 'Linear pricing', href: 'https://linear.app/pricing' },
					{ label: 'Linear documentation', href: 'https://linear.app/docs' },
					{ label: 'Strifira source and documentation', href: 'https://github.com/SephiraCS/Strifira' }
				]
			}
		],
		footnotes:
			'Last reviewed: July 23, 2026. Methodology: first-party product documentation was compared with Strifira’s repository and current implementation. This is a snapshot, not an endorsement; features and pricing change. Strifira is developed by Sephira and is not affiliated with Linear.'
	},

	'strifira-vs-plane': {
		slug: 'strifira-vs-plane',
		title: 'Strifira vs Plane — Comparison',
		description:
			'Compare Strifira and Plane by license, self-hosting, product scope, planning features, integrations, feature tiers, and operational fit.',
		heading: 'Strifira vs Plane',
		intro:
			'Both products can run on your infrastructure, but they solve different-sized problems. Plane is a broad project-management suite with commercial plans; Strifira is a focused issue tracker distributed as one Apache 2.0 edition.',
		sections: [
			{
				heading: 'License and editions',
				body: 'Both projects publish source, but their licenses and edition models differ.',
				list: [
					'Plane’s public repository uses AGPL-3.0. Plane offers cloud and self-hosted products with paid feature plans.',
					'Strifira uses Apache 2.0 and ships one self-hosted edition. It has no paid feature plan or enterprise repository.'
				]
			},
			{
				heading: 'Scope and focus',
				body: 'Plane covers more workflows; Strifira deliberately covers fewer.',
				list: [
					'Plane includes work items, cycles, modules, initiatives, pages, wiki, intake, dashboards and multiple layouts.',
					'Strifira focuses on issues, cycles, projects, saved views, analytics insights, public sharing and GitHub activity. It has no wiki, modules or customizable dashboards.'
				]
			},
			{
				heading: 'Core issue tracking',
				body: 'Both support structured work, but their ownership and customization models differ.',
				list: [
					'Plane: documents multiple work-item views, properties, and project-management workflows.',
					'Strifira includes multiple assignees, sub-issues, relations, hierarchical labels, issue templates, favorites and read-only public links.'
				]
			},
			{
				heading: 'Planning and visualization',
				body: 'Plane offers more layouts and planning layers. Strifira keeps planning closer to issues.',
				list: [
					'Plane: cycles, modules, multiple layout views (list, board, Gantt, calendar).',
					'Strifira: cycles with burndown/velocity, projects with Gantt view, list and board issue views, and a built-in insights page with burn-up trends.'
				]
			},
			{
				heading: 'Integrations',
				body: 'Plane documents a wider integration and migration catalog.',
				list: [
					'Plane: GitHub, GitLab, Slack integrations. Importer for multiple platforms. API and webhooks.',
					'Strifira: self-configuring GitHub App with auto-linking, auto-transitions, and real-time WebSocket events. Webhooks. Unreleased opt-in Dev Machines development. Import/export not yet available.'
				]
			},
			{
				heading: 'How to choose',
				body: 'The relevant tradeoff is breadth versus a smaller, uniform edition.',
				list: [
					'Choose Plane when pages, wiki, modules, dashboards, importers or its wider integration catalog are required.',
					'Choose Strifira when a focused tracker, multiple assignees, Apache 2.0, and one ungated self-hosted edition matter more than breadth.'
				]
			},
			{
				heading: 'Sources',
				body: 'Review the first-party sources for the latest edition, licensing, and feature details.',
				links: [
					{ label: 'Plane documentation', href: 'https://docs.plane.so/' },
					{ label: 'Plane source repository', href: 'https://github.com/makeplane/plane' },
					{ label: 'Strifira source and documentation', href: 'https://github.com/SephiraCS/Strifira' }
				]
			}
		],
		footnotes:
			'Last reviewed: July 23, 2026. Methodology: first-party documentation and repositories were compared with Strifira’s current implementation. This is a snapshot, not an endorsement; features, editions, and licensing can change. Strifira is not affiliated with Plane.'
	}
};

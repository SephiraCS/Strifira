/**
 * Curated lucide icons a kanban column can pick from. Keys are lucide icon
 * names (kebab-case) stored on `team_statuses.icon`; the board and the
 * settings page render the matching component through `columnIcon`.
 */
import Archive from '@lucide/svelte/icons/archive';
import Bug from '@lucide/svelte/icons/bug';
import Circle from '@lucide/svelte/icons/circle';
import CircleCheck from '@lucide/svelte/icons/circle-check';
import CircleDashed from '@lucide/svelte/icons/circle-dashed';
import CircleDot from '@lucide/svelte/icons/circle-dot';
import CircleX from '@lucide/svelte/icons/circle-x';
import Clock from '@lucide/svelte/icons/clock';
import Compass from '@lucide/svelte/icons/compass';
import Contrast from '@lucide/svelte/icons/contrast';
import Eye from '@lucide/svelte/icons/eye';
import Flag from '@lucide/svelte/icons/flag';
import Flame from '@lucide/svelte/icons/flame';
import GitBranch from '@lucide/svelte/icons/git-branch';
import Hammer from '@lucide/svelte/icons/hammer';
import Heart from '@lucide/svelte/icons/heart';
import Lightbulb from '@lucide/svelte/icons/lightbulb';
import Loader from '@lucide/svelte/icons/loader';
import MessageSquare from '@lucide/svelte/icons/message-square';
import Package from '@lucide/svelte/icons/package';
import Pause from '@lucide/svelte/icons/pause';
import Play from '@lucide/svelte/icons/play';
import Rocket from '@lucide/svelte/icons/rocket';
import Search from '@lucide/svelte/icons/search';
import ShieldCheck from '@lucide/svelte/icons/shield-check';
import Star from '@lucide/svelte/icons/star';
import Tag from '@lucide/svelte/icons/tag';
import Target from '@lucide/svelte/icons/target';
import Trophy from '@lucide/svelte/icons/trophy';
import Users from '@lucide/svelte/icons/users';
import Wrench from '@lucide/svelte/icons/wrench';
import Zap from '@lucide/svelte/icons/zap';
import type { Component } from 'svelte';

export const COLUMN_ICONS: Record<string, Component<{ size?: number }>> = {
	archive: Archive,
	bug: Bug,
	circle: Circle,
	'circle-check': CircleCheck,
	'circle-dashed': CircleDashed,
	'circle-dot': CircleDot,
	'circle-x': CircleX,
	clock: Clock,
	compass: Compass,
	contrast: Contrast,
	eye: Eye,
	flag: Flag,
	flame: Flame,
	'git-branch': GitBranch,
	hammer: Hammer,
	heart: Heart,
	lightbulb: Lightbulb,
	loader: Loader,
	'message-square': MessageSquare,
	package: Package,
	pause: Pause,
	play: Play,
	rocket: Rocket,
	search: Search,
	'shield-check': ShieldCheck,
	star: Star,
	tag: Tag,
	target: Target,
	trophy: Trophy,
	users: Users,
	wrench: Wrench,
	zap: Zap
};

export function columnIcon(name: string | null | undefined): Component<{ size?: number }> | null {
	if (!name) return null;
	return COLUMN_ICONS[name] ?? null;
}

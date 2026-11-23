import type { APIRoute } from 'astro';
import { release } from '../data/release';
import { releasePage } from '../data/site';

/**
 * release.json: the newest released version, for the v8s update check (agent/internal/updatecheck, every 15
 * minutes). `version` is the contract (stable semver, no "v"); the agent builds its own release URL and ignores `url`.
 * Built from the CHANGELOG like the rest of the site, and deployed only after the GitHub release exists.
 */
export const GET: APIRoute = () =>
  new Response(`${JSON.stringify({ version: release.version, date: release.date, url: releasePage(release.version) })}\n`, {
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
  });

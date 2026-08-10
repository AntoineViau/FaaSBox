import { Routes, type UrlMatchResult, type UrlSegment } from '@angular/router';

import { ApiKeysComponent } from '@/api-keys/api-keys.component';
import { authGuard } from '@/auth/auth.guard';
import { LoginComponent } from '@/auth/login.component';
import { EditorComponent } from '@/editor/editor.component';

/**
 * Serves the three editor forms — `/editor`, `/editor/:id` and
 * `/editor/:id/tab/:tab` — from one single Route.
 *
 * Three separate Route entries would be the obvious spelling and the wrong one:
 * Angular only reuses a component instance while the same route configuration
 * stays active, so moving between tabs would destroy and rebuild
 * EditorComponent — and with it the localName, localScript and localPackageJson
 * buffers — on every click.
 *
 * The literal `tab/` segment is kept on purpose: it reserves the room for a
 * future `/editor/:id/logs` without ever colliding with a tab slug.
 */
export function editorMatcher(segments: UrlSegment[]): UrlMatchResult | null {
  if (segments.length === 0 || segments[0].path !== 'editor') return null;
  if (segments.length === 1) return { consumed: segments, posParams: {} };
  if (segments.length === 2) return { consumed: segments, posParams: { id: segments[1] } };
  if (segments.length === 4 && segments[2].path === 'tab') {
    return { consumed: segments, posParams: { id: segments[1], tab: segments[3] } };
  }
  return null;
}

/**
 * Every route is eager. The application is small enough that lazy chunks buy
 * nothing, while they cost a whole failure mode: a rebuild renames the chunks,
 * and a browser still holding the previous page asks for a file that no longer
 * exists - "Failed to fetch dynamically imported module". One bundle cannot
 * fall out of step with itself.
 */
export const routes: Routes = [
  {
    path: 'login',
    component: LoginComponent,
    title: 'FaaSBox - Login',
  },
  {
    // Not 'functions': the server exposes GET /functions as an API route, which
    // wins over the SPA fallback. Any full page load there answers
    // {"error":"Missing X-API-Key header"} instead of the editor - a browser
    // navigation carries no X-API-Key header, valid session or not. Nothing on
    // the server starts with /editor, so the nested forms are safe.
    matcher: editorMatcher,
    component: EditorComponent,
    canActivate: [authGuard],
    title: 'FaaSBox',
  },
  {
    path: 'keys',
    component: ApiKeysComponent,
    canActivate: [authGuard],
    title: 'FaaSBox - API keys',
  },
  {
    path: '',
    redirectTo: '/editor',
    pathMatch: 'full',
  },
  {
    path: '**',
    redirectTo: '/editor',
  },
];

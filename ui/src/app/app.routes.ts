import { Routes } from '@angular/router';

import { ApiKeysComponent } from '@/api-keys/api-keys.component';
import { authGuard } from '@/auth/auth.guard';
import { LoginComponent } from '@/auth/login.component';
import { EditorComponent } from '@/editor/editor.component';

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
    // navigation carries no X-API-Key header, valid session or not.
    path: 'editor',
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

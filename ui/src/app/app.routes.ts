import { Routes } from '@angular/router';
import { authGuard } from '@/auth/auth.guard';

export const routes: Routes = [
  {
    path: 'login',
    loadComponent: () =>
      import('@/auth/login.component').then((m) => m.LoginComponent),
    title: 'FaaSBox - Login',
  },
  {
    // Not 'functions': the server exposes GET /functions as an API route, which
    // wins over the SPA fallback. Any full page load there answers
    // {"error":"Missing X-API-Key header"} instead of the editor - a browser
    // navigation carries no X-API-Key header, valid session or not.
    path: 'editor',
    loadComponent: () =>
      import('@/editor/editor.component').then((m) => m.EditorComponent),
    canActivate: [authGuard],
    title: 'FaaSBox',
  },
  {
    path: 'keys',
    loadComponent: () =>
      import('@/api-keys/api-keys.component').then((m) => m.ApiKeysComponent),
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

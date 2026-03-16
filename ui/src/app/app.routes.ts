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
    path: 'functions',
    loadComponent: () =>
      import('@/editor/editor.component').then((m) => m.EditorComponent),
    canActivate: [authGuard],
    title: 'FaaSBox',
  },
  {
    path: '',
    redirectTo: '/functions',
    pathMatch: 'full',
  },
  {
    path: '**',
    redirectTo: '/functions',
  },
];

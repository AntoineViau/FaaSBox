import { Injectable, signal, computed, inject } from '@angular/core';
import { Router } from '@angular/router';

interface AuthResponse {
  token: string;
  record: {
    id: string;
    email: string;
  };
}

const TOKEN_KEY = 'faasbox_token';

@Injectable({ providedIn: 'root' })
export class AuthService {
  private readonly router = inject(Router);
  private readonly token = signal<string | null>(this.loadToken());

  readonly isAuthenticated = computed(() => {
    const t = this.token();
    return t !== null && !this.isTokenExpired(t);
  });

  getToken(): string | null {
    const t = this.token();
    if (t && this.isTokenExpired(t)) {
      this.clearToken();
      return null;
    }
    return t;
  }

  async login(email: string, password: string): Promise<void> {
    const response = await fetch('/api/collections/_superusers/auth-with-password', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ identity: email, password }),
    });

    if (!response.ok) {
      const error = await response.json().catch(() => ({}));
      throw new Error(error.message || 'Authentication failed');
    }

    const data: AuthResponse = await response.json();
    this.token.set(data.token);
    localStorage.setItem(TOKEN_KEY, data.token);
  }

  logout(): void {
    this.clearToken();
    this.router.navigate(['/login']);
  }

  private clearToken(): void {
    this.token.set(null);
    localStorage.removeItem(TOKEN_KEY);
  }

  private loadToken(): string | null {
    const token = localStorage.getItem(TOKEN_KEY);
    if (token && this.isTokenExpired(token)) {
      localStorage.removeItem(TOKEN_KEY);
      return null;
    }
    return token;
  }

  private isTokenExpired(token: string): boolean {
    try {
      const payload = JSON.parse(atob(token.split('.')[1]));
      return payload.exp * 1000 < Date.now();
    } catch {
      return true;
    }
  }
}

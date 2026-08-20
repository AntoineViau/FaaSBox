import {
  ApplicationConfig,
  inject,
  provideAppInitializer,
  provideBrowserGlobalErrorListeners,
} from '@angular/core';
import { provideRouter } from '@angular/router';
import { provideHttpClient, withInterceptors } from '@angular/common/http';
import { routes } from './app.routes';
import { authInterceptor } from '@/auth/auth.interceptor';
import { InstanceService } from '@/instance/instance.service';
import { provideZard } from '@shared/core/provider/providezard';

export const appConfig: ApplicationConfig = {
  providers: [
    provideBrowserGlobalErrorListeners(),
    provideRouter(routes),
    provideHttpClient(withInterceptors([authInterceptor])),
    // Before the first render: a button that appeared usable and then went grey
    // the moment the answer landed would read as a glitch. A failure is not
    // fatal — the service falls back to a normal instance and says so in the
    // console; the server refuses the writes either way.
    provideAppInitializer(() => inject(InstanceService).load()),
    provideZard(),
  ],
};

import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import App from './app/App.tsx';
import { AppProviders } from './app/providers.tsx';
import './index.css';

const root = document.getElementById('root');
if (!root) throw new Error('cix-dashboard: #root not found in index.html');

// React Router lives at /dashboard so all in-app paths are relative to that
// prefix. The Go server returns the same index.html for any /dashboard/*
// URL so a deep refresh still boots the SPA, then BrowserRouter takes over.
createRoot(root).render(
  <StrictMode>
    <BrowserRouter basename="/dashboard">
      <AppProviders>
        <App />
      </AppProviders>
    </BrowserRouter>
  </StrictMode>
);

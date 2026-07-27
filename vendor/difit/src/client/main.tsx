import React from 'react';
import ReactDOM from 'react-dom/client';
import { HotkeysProvider } from 'react-hotkeys-hook';

import { WorkspaceShell } from './WorkspaceShell';
import './styles/global.css';

const rootElement = document.getElementById('root');

if (!rootElement) {
  throw new Error('Root element not found');
}

ReactDOM.createRoot(rootElement).render(
  <React.StrictMode>
    <HotkeysProvider initiallyActiveScopes={['navigation']}>
      <WorkspaceShell />
    </HotkeysProvider>
  </React.StrictMode>,
);

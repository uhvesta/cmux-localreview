import React from 'react';
import ReactDOM from 'react-dom/client';
import { HotkeysProvider } from 'react-hotkeys-hook';

import { WorkspaceShell } from './WorkspaceShell';
import { QueueHome } from './QueueHome';
import { captureDaemonTokenFromLocation } from './services/daemonAuth';
import './styles/global.css';

const rootElement = document.getElementById('root');

if (!rootElement) {
  throw new Error('Root element not found');
}

// Queue Home and the workspace reviewer issue authenticated daemon requests
// during their first effects. Move a fragment token into session storage before
// React renders so those requests cannot race a later capture effect.
captureDaemonTokenFromLocation();

ReactDOM.createRoot(rootElement).render(
  <React.StrictMode>
    <HotkeysProvider initiallyActiveScopes={['navigation']}>
      {window.location.pathname === '/review' ? <WorkspaceShell /> : <QueueHome />}
    </HotkeysProvider>
  </React.StrictMode>,
);

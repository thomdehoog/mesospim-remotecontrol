// Application bootstrap: initialize the router (URL → store), the
// WebSocket session and initial data loading, then mount the shell.

import './components/app-shell';
import { initRouter } from './router';
import { initSession } from './ws';
import { loadTree, refreshDetail, refreshStatus } from './actions';
import { store } from './store';

initRouter();
initSession();

// Initial load reconstructs the application state encoded in the URL.
refreshStatus();
loadTree();
if (store.get().selected) refreshDetail();

// Navigation changes triggered by back/forward reload their context.
window.addEventListener('popstate', () => {
  loadTree();
  refreshDetail();
});

// Periodic status refresh keeps the revision indicator current.
setInterval(refreshStatus, 30_000);

// WebSocket session client — transient runtime information only:
// presence, repository events, maintenance mode and indexing progress.

import { store } from './store';
import { loadTree, refreshDetail, refreshStatus } from './actions';

let socket: WebSocket | null = null;
let reconnectTimer: number | undefined;

const user = `user-${Math.random().toString(36).slice(2, 7)}`;

export function initSession(): void {
  connect();
  // Report which artifact this session is viewing, and whether it is being
  // edited, so other sessions see live presence.
  store.subscribe((state, changed) => {
    if (socket?.readyState !== WebSocket.OPEN) return;
    if (changed.has('selected')) {
      socket.send(JSON.stringify({ type: 'viewing', guid: state.selected, editing: state.editing }));
    }
    if (changed.has('editing')) {
      socket.send(JSON.stringify({ type: 'editing', editing: state.editing }));
    }
  });
}

function connect(): void {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  socket = new WebSocket(`${proto}://${location.host}/api/ws?user=${user}`);
  socket.onopen = () => {
    const { selected, editing } = store.get();
    if (selected) socket?.send(JSON.stringify({ type: 'viewing', guid: selected, editing }));
  };
  socket.onmessage = (ev) => {
    try {
      handle(JSON.parse(ev.data));
    } catch { /* ignore malformed frames */ }
  };
  socket.onclose = () => {
    socket = null;
    clearTimeout(reconnectTimer);
    reconnectTimer = window.setTimeout(connect, 2000);
  };
}

interface SessionMessage {
  type: string;
  users?: { user: string; viewing?: string; editing?: boolean }[];
  event?: { type: string; guid?: string; path?: string; detail?: string };
}

function handle(msg: SessionMessage): void {
  if (msg.type === 'presence' && msg.users) {
    store.update({ presence: msg.users });
    return;
  }
  if (msg.type !== 'event' || !msg.event) return;
  const e = msg.event;
  switch (e.type) {
    case 'maintenance':
      store.update({ maintenance: e.detail === 'enabled' });
      refreshStatus();
      break;
    case 'reindex':
      store.update({ notice: `Reindexing — ${e.detail ?? ''}` });
      refreshStatus();
      break;
    case 'workflow-transition':
    case 'artifact-updated':
      if (e.guid && e.guid === store.get().selected) {
        // Another session changed the artifact currently open here. Reload
        // to show the change — but not while there are unsaved local edits,
        // which a reload would silently discard. In that case keep the edits
        // and warn; saving will surface the conflict (409) to resolve.
        if (store.get().editing) {
          store.update({
            notice: 'This artifact was changed by another session. Your unsaved edits are kept — save to attempt to apply them, or discard to reload.',
          });
        } else {
          refreshDetail();
        }
      }
      loadTree();
      break;
    default:
      // Repository changed: refresh the navigation context.
      loadTree();
  }
}

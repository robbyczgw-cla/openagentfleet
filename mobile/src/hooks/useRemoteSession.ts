import { useCallback, useEffect, useRef, useState } from "react";
import { AppState, type AppStateStatus } from "react-native";

import { isEventCursor, RemoteApiError, RemoteClient } from "../api/RemoteClient";
import type { Bootstrap, ConnectionState, PairingBundle, RemoteProfile } from "../api/types";
import { connectionProfileStore, secureTokenStore } from "../auth/secureStore";

const MAX_RECONNECT_DELAY_MS = 30_000;

/** Pure lifecycle helper so suspended/background apps never hold an SSE socket. */
export function isForegroundAppState(appState: AppStateStatus): boolean {
  return appState === "active";
}

function cursorFromBootstrap(bootstrap: Bootstrap): number | undefined {
  return isEventCursor(bootstrap.event_cursor) ? bootstrap.event_cursor : undefined;
}

export function useRemoteSession() {
  const [profile, setProfile] = useState<RemoteProfile | null>(null);
  const [client, setClient] = useState<RemoteClient | null>(null);
  const [state, setState] = useState<ConnectionState>("disconnected");
  const [bootstrap, setBootstrap] = useState<Bootstrap | null>(null);
  const [error, setError] = useState<string | null>(null);
  const cursor = useRef<number | undefined>(undefined);
  const reconnectTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const reconnectAttempt = useRef(0);
  const [streamEpoch, setStreamEpoch] = useState(0);
  const [appState, setAppState] = useState<AppStateStatus>(AppState.currentState);

  const refresh = useCallback(async (activeClient = client, conversationID?: string, resetCursor = false) => {
    if (!activeClient) return undefined;
    const next = await activeClient.bootstrap(conversationID);
    const snapshotCursor = cursorFromBootstrap(next);
    if (snapshotCursor !== undefined || resetCursor) cursor.current = snapshotCursor;
    setBootstrap(next);
    return next;
  }, [client]);

  const resume = useCallback(async (nextProfile: RemoteProfile, token: string) => {
    const nextClient = new RemoteClient(nextProfile, token);
    const nextBootstrap = await nextClient.bootstrap();
    cursor.current = cursorFromBootstrap(nextBootstrap);
    setProfile(nextProfile);
    setClient(nextClient);
    setBootstrap(nextBootstrap);
    setState("connected");
  }, []);

  const pair = useCallback(async (bundle: PairingBundle, deviceName: string, platform: string) => {
    setState("connecting");
    setError(null);
    try {
      const paired = await RemoteClient.pair(bundle, deviceName, platform);
      const nextClient = new RemoteClient(paired.profile, paired.token);
      const nextBootstrap = await nextClient.bootstrap();
      await Promise.all([connectionProfileStore.saveProfile(paired.profile), secureTokenStore.saveToken(paired.token)]);
      cursor.current = cursorFromBootstrap(nextBootstrap);
      setProfile(paired.profile);
      setClient(nextClient);
      setBootstrap(nextBootstrap);
      setState("connected");
    } catch (cause) {
      setState("disconnected");
      setError(cause instanceof Error ? cause.message : "Could not pair with this Mac.");
      throw cause;
    }
  }, []);

  const disconnect = useCallback(async () => {
    const activeClient = client;
    if (reconnectTimer.current) clearTimeout(reconnectTimer.current);
    // Remove live access before waiting on an optional best-effort server logout.
    setProfile(null);
    setClient(null);
    setBootstrap(null);
    setState("disconnected");
    setError(null);
    cursor.current = undefined;
    setStreamEpoch((value) => value + 1);
    await Promise.all([
      connectionProfileStore.clear(),
      activeClient?.logout().catch(() => undefined)
    ]);
  }, [client]);

  useEffect(() => {
    void (async () => {
      const [savedProfile, token] = await Promise.all([connectionProfileStore.readProfile(), secureTokenStore.readToken()]);
      if (!savedProfile || !token) return;
      setState("connecting");
      try {
        await resume(savedProfile, token);
      } catch {
        await connectionProfileStore.clear();
        setState("disconnected");
      }
    })();
  }, [resume]);

  useEffect(() => {
    const subscription = AppState.addEventListener("change", setAppState);
    return () => subscription.remove();
  }, []);

  useEffect(() => {
    if (client && !isForegroundAppState(appState)) setState("degraded");
  }, [appState, client]);

  useEffect(() => {
    if (!client || !bootstrap?.conversation.id || !isForegroundAppState(appState)) return;
    let closed = false;
    let unsubscribe: (() => void) | undefined;
    const scheduleReconnect = () => {
      if (closed || reconnectTimer.current) return;
      const delay = Math.min(1_000 * 2 ** reconnectAttempt.current++, MAX_RECONNECT_DELAY_MS);
      reconnectTimer.current = setTimeout(() => {
        reconnectTimer.current = undefined;
        start();
      }, delay);
    };
    const resetFromServer = () => {
      unsubscribe?.();
      cursor.current = undefined;
      setState("degraded");
      void refresh(client, bootstrap.conversation.id, true)
        .then(() => setStreamEpoch((value) => value + 1))
        .catch(() => scheduleReconnect());
    };
    const start = () => {
      if (closed) return;
      unsubscribe?.();
      unsubscribe = client.subscribe(bootstrap.conversation.id, cursor.current, (remoteEvent) => {
        if (closed) return;
        if (remoteEvent.kind === "open") {
          reconnectAttempt.current = 0;
          setState("connected");
          return;
        }
        if (remoteEvent.kind === "reset") {
          resetFromServer();
          return;
        }
        if (remoteEvent.kind === "event") {
          cursor.current = remoteEvent.event.cursor;
          void refresh(client, bootstrap.conversation.id).catch(() => undefined);
          return;
        }
        setState("degraded");
        unsubscribe?.();
        scheduleReconnect();
      });
    };
    start();
    return () => {
      closed = true;
      unsubscribe?.();
      if (reconnectTimer.current) clearTimeout(reconnectTimer.current);
    };
  }, [appState, bootstrap?.conversation.id, client, refresh, streamEpoch]);

  const send = useCallback(async (content: string) => {
    if (!client || !bootstrap) throw new RemoteApiError("Pair this device with your Mac first.");
    await client.sendMessage(bootstrap.conversation.id, content.trim());
    await refresh(client, bootstrap.conversation.id);
  }, [bootstrap, client, refresh]);

  const selectConversation = useCallback(async (conversationID: string) => {
    if (!client) throw new RemoteApiError("Pair this device with your Mac first.");
    cursor.current = undefined;
    await refresh(client, conversationID);
    setStreamEpoch((value) => value + 1);
  }, [client, refresh]);

  return { profile, client, state, bootstrap, error, pair, disconnect, refresh, send, selectConversation, setError };
}

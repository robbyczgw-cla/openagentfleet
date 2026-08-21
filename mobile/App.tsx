import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Alert, KeyboardAvoidingView, Platform, SafeAreaView, StatusBar, StyleSheet, Text, View } from "react-native";

import { defaultDeviceName, parsePairingBundle } from "./src/api/pairing";
import type { MobileApproval, MobileRoutine, MobileRun } from "./src/api/types";
import { useKeyboardOpen } from "./src/hooks/useKeyboardOpen";
import { useRemoteSession } from "./src/hooks/useRemoteSession";
import { ChatScreen } from "./src/screens/ChatScreen";
import { ComputerScreen } from "./src/screens/ComputerScreen";
import { PairingScreen } from "./src/screens/PairingScreen";
import { RoutinesScreen } from "./src/screens/RoutinesScreen";
import { SettingsScreen } from "./src/screens/SettingsScreen";
import { activeRun, allowOptionID, canControlDevice } from "./src/session";
import { type ThemeColors, useStatusBarStyle, useTheme } from "./src/theme";
import { type Screen, StatusBanner, StatusPill, TabBar } from "./src/ui";

export default function App() {
  const remote = useRemoteSession();
  const theme = useTheme();
  const barStyle = useStatusBarStyle();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const [screen, setScreen] = useState<Screen>("chat");
  const [composer, setComposer] = useState("");
  const [pairingBundle, setPairingBundle] = useState("");
  const [connecting, setConnecting] = useState(false);
  const [sending, setSending] = useState(false);
  const [scanning, setScanning] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [frame, setFrame] = useState<string | undefined>(undefined);
  const [frameLoading, setFrameLoading] = useState(false);
  const [frameError, setFrameError] = useState<string | undefined>(undefined);
  const [computerControlHeld, setComputerControlHeld] = useState(false);
  const [computerControlBusy, setComputerControlBusy] = useState(false);
  const [actionBusy, setActionBusy] = useState(false);
  const [routines, setRoutines] = useState<MobileRoutine[]>([]);
  const [routinesError, setRoutinesError] = useState<string | undefined>(undefined);
  const autoFrameClient = useRef<typeof remote.client>(null);
  const canControl = canControlDevice(remote.profile);
  const keyboardOpen = useKeyboardOpen();
  const computer = remote.bootstrap?.computer;

  const refreshFrame = useCallback(async () => {
    if (!remote.client) return;
    setFrameLoading(true);
    setFrameError(undefined);
    try {
      setFrame(await remote.client.fetchFrameDataUri());
    } catch (error) {
      setFrame(undefined);
      setFrameError(error instanceof Error ? error.message : "Could not load the computer frame.");
    } finally {
      setFrameLoading(false);
    }
  }, [remote.client]);

  useEffect(() => {
    autoFrameClient.current = null;
    setFrame(undefined);
    setFrameError(undefined);
    setComputerControlHeld(false);
  }, [remote.client]);

  useEffect(() => {
    if (screen !== "computer" || !remote.client || autoFrameClient.current === remote.client) return;
    autoFrameClient.current = remote.client;
    void refreshFrame();
  }, [refreshFrame, remote.client, screen]);

  const pair = async (rawBundle = pairingBundle) => {
    setConnecting(true);
    try {
      const bundle = parsePairingBundle(rawBundle);
      await remote.pair(bundle, defaultDeviceName(Platform.OS), Platform.OS);
      setScanning(false);
      setScreen("chat");
    } catch (error) {
      Alert.alert("Pairing failed", error instanceof Error ? error.message : "Create a fresh pairing QR on the desktop app and try again.");
    } finally {
      setPairingBundle("");
      setConnecting(false);
    }
  };

  const send = async () => {
    if (!composer.trim()) return;
    const text = composer;
    setComposer("");
    setSending(true);
    try {
      await remote.send(text);
    } catch (error) {
      setComposer(text);
      Alert.alert("Message not sent", error instanceof Error ? error.message : "Try again.");
    } finally {
      setSending(false);
    }
  };

  const refreshChat = async () => {
    if (!remote.client) return;
    setRefreshing(true);
    try {
      await remote.refresh(remote.client, remote.bootstrap?.conversation.id);
    } catch (error) {
      Alert.alert("Could not refresh", error instanceof Error ? error.message : "Try again.");
    } finally {
      setRefreshing(false);
    }
  };

  const toggleComputerControl = async () => {
    if (!remote.client) return;
    setComputerControlBusy(true);
    try {
      const status = await remote.client.setComputerControl(!computerControlHeld);
      setComputerControlHeld(Boolean(status.control_held));
      if (status.control_held) await refreshFrame();
    } catch (error) {
      setComputerControlHeld(false);
      Alert.alert("Computer control unavailable", error instanceof Error ? error.message : "Take control from the desktop app and try again.");
    } finally {
      setComputerControlBusy(false);
    }
  };

  const loadRoutines = useCallback(async () => {
    if (!remote.client) return;
    try {
      setRoutines(await remote.client.routines());
      setRoutinesError(undefined);
    } catch (error) {
      setRoutinesError(error instanceof Error ? error.message : "Could not load routines.");
    }
  }, [remote.client]);

  useEffect(() => {
    if (screen !== "routines" || !remote.client) return;
    void loadRoutines();
  }, [loadRoutines, remote.client, screen]);

  const mutate = async (work: () => Promise<void>, failedTitle: string) => {
    if (actionBusy) return;
    setActionBusy(true);
    try {
      await work();
    } catch (error) {
      Alert.alert(failedTitle, error instanceof Error ? error.message : "Try again.");
    } finally {
      setActionBusy(false);
    }
  };

  const resolveApproval = (approval: MobileApproval, status: "approved" | "denied") => {
    const client = remote.client;
    if (!client) return;
    void mutate(async () => {
      await client.resolveApproval(approval.id, status, status === "approved" ? allowOptionID(approval) : "");
      await remote.refresh(client, remote.bootstrap?.conversation.id);
    }, "Approval not sent");
  };

  const stopActiveRun = (run: MobileRun) => {
    const client = remote.client;
    if (!client) return;
    void mutate(async () => {
      await client.stopRun(run.id);
      await remote.refresh(client, remote.bootstrap?.conversation.id);
    }, "Could not stop the run");
  };

  const pauseRoutine = (routine: MobileRoutine) => {
    if (!remote.client) return;
    void mutate(async () => {
      await remote.client?.pauseRoutine(routine.id, "paused from phone");
      await loadRoutines();
    }, "Could not pause routine");
  };

  const enableRoutine = (routine: MobileRoutine) => {
    if (!remote.client) return;
    void mutate(async () => {
      await remote.client?.enableRoutine(routine.id);
      await loadRoutines();
    }, "Could not enable routine");
  };

  const clickComputer = async (x: number, y: number) => {
    if (!remote.client || !computerControlHeld || computerControlBusy) return;
    setComputerControlBusy(true);
    try {
      const status = await remote.client.browserAction({ action: "click", x, y });
      setComputerControlHeld(Boolean(status.control_held));
      await refreshFrame();
    } catch (error) {
      setComputerControlHeld(false);
      Alert.alert("Computer action failed", error instanceof Error ? error.message : "The remote computer rejected the click.");
    } finally {
      setComputerControlBusy(false);
    }
  };

  if (!remote.profile) {
    return (
      <PairingScreen
        pairingBundle={pairingBundle}
        setPairingBundle={setPairingBundle}
        connecting={connecting}
        scanning={scanning}
        setScanning={setScanning}
        error={remote.error}
        onPair={() => void pair()}
        onScanned={(value) => void pair(value)}
      />
    );
  }

  const pending = remote.bootstrap?.approvals || [];
  const conversationID = remote.bootstrap?.conversation.id;
  const conversationApprovals = pending.filter((item) => item.conversation_id === conversationID && item.status === "pending");
  const otherApprovalCount = pending.filter((item) => item.conversation_id !== conversationID && item.status === "pending").length;
  const running = activeRun(remote.bootstrap?.runs);
  const title =
    screen === "chat"
      ? remote.bootstrap?.conversation.title || "Agent"
      : screen === "computer"
        ? "Computer"
        : screen === "routines"
          ? "Routines"
          : "Settings";

  return (
    <SafeAreaView style={styles.safe}>
      <StatusBar barStyle={barStyle} backgroundColor={theme.background} />
      <View style={styles.header}>
        <View style={{ flex: 1, paddingRight: 12 }}>
          <Text style={styles.eyebrow}>OpenAgentFleet</Text>
          <Text numberOfLines={1} style={styles.title}>
            {title}
          </Text>
        </View>
        <StatusPill state={remote.state} />
      </View>
      <StatusBanner state={remote.state} />
      <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === "ios" ? "padding" : undefined}>
        {screen === "chat" && (
          <ChatScreen
            conversations={remote.bootstrap?.conversations || []}
            activeID={conversationID}
            messages={remote.bootstrap?.messages || []}
            approvals={conversationApprovals}
            otherApprovalCount={otherApprovalCount}
            activeRun={running}
            canControl={canControl}
            composer={composer}
            onChange={setComposer}
            onSend={send}
            onSelectConversation={(id) =>
              void remote.selectConversation(id).catch((error) =>
                Alert.alert("Could not open conversation", error instanceof Error ? error.message : "Try again."),
              )
            }
            onResolve={resolveApproval}
            onStop={stopActiveRun}
            onRefresh={() => void refreshChat()}
            refreshing={refreshing}
            sending={sending || actionBusy}
          />
        )}
        {screen === "computer" && (
          <ComputerScreen
            computer={computer}
            frame={frame}
            frameError={frameError}
            loading={frameLoading}
            controlHeld={computerControlHeld}
            controlBusy={computerControlBusy}
            onToggleControl={() => void toggleComputerControl()}
            onClick={clickComputer}
            onRefresh={() => void refreshFrame()}
          />
        )}
        {screen === "routines" && (
          <RoutinesScreen
            routines={routines}
            error={routinesError}
            canControl={canControl}
            busy={actionBusy}
            onPause={pauseRoutine}
            onEnable={enableRoutine}
            onRefresh={() => void loadRoutines()}
          />
        )}
        {screen === "settings" && (
          <SettingsScreen profile={remote.profile} state={remote.state} computer={computer} onDisconnect={() => void remote.disconnect()} />
        )}
      </KeyboardAvoidingView>
      {!keyboardOpen ? <TabBar screen={screen} onChange={setScreen} /> : null}
    </SafeAreaView>
  );
}

function createStyles(theme: ThemeColors) {
  return StyleSheet.create({
    safe: { flex: 1, backgroundColor: theme.background, paddingTop: Platform.OS === "android" ? StatusBar.currentHeight || 0 : 0 },
    header: {
      paddingHorizontal: 20,
      paddingTop: 12,
      paddingBottom: 12,
      borderBottomWidth: StyleSheet.hairlineWidth,
      borderColor: theme.border,
      flexDirection: "row",
      justifyContent: "space-between",
      alignItems: "center"
    },
    eyebrow: { color: theme.textMuted, fontSize: 12, fontWeight: "700", letterSpacing: 0.2 },
    title: { color: theme.text, fontSize: 22, fontWeight: "700", marginTop: 2 }
  });
}

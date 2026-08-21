import { useCallback, useEffect, useRef, useState } from "react";
import {
  ActivityIndicator,
  Alert,
  FlatList,
  type GestureResponderEvent,
  Image,
  Keyboard,
  KeyboardAvoidingView,
  Platform,
  Pressable,
  SafeAreaView,
  ScrollView,
  StatusBar,
  StyleSheet,
  Text,
  TextInput,
  View
} from "react-native";
import { CameraView, useCameraPermissions } from "expo-camera";

import { defaultDeviceName, parsePairingBundle } from "./src/api/pairing";
import type { ComputerStatus, Conversation, MobileApproval, MobileRoutine, MobileRun, RemoteProfile } from "./src/api/types";
import { useRemoteSession } from "./src/hooks/useRemoteSession";

type Screen = "chat" | "computer" | "routines" | "settings";

const TERMINAL_RUN_STATUS = new Set(["completed", "failed", "stopped", "blocked"]);

function canControlDevice(profile: RemoteProfile | null): boolean {
  return profile?.device.scope_profile !== "observer";
}

function activeRun(runs: MobileRun[] | undefined): MobileRun | undefined {
  return [...(runs || [])].reverse().find((run) => !TERMINAL_RUN_STATUS.has(run.status));
}

function allowOptionID(approval: MobileApproval): string {
  const allow = (approval.options || []).find((option) => (option.kind || "").includes("allow") || option.optionId.includes("allow"));
  return allow?.optionId || approval.options?.[0]?.optionId || "";
}

function useKeyboardOpen() {
  const [open, setOpen] = useState(false);
  useEffect(() => {
    const show = Keyboard.addListener(Platform.OS === "ios" ? "keyboardWillShow" : "keyboardDidShow", () => setOpen(true));
    const hide = Keyboard.addListener(Platform.OS === "ios" ? "keyboardWillHide" : "keyboardDidHide", () => setOpen(false));
    return () => {
      show.remove();
      hide.remove();
    };
  }, []);
  return open;
}

export default function App() {
  const remote = useRemoteSession();
  const [screen, setScreen] = useState<Screen>("chat");
  const [composer, setComposer] = useState("");
  const [pairingBundle, setPairingBundle] = useState("");
  const [connecting, setConnecting] = useState(false);
  const [sending, setSending] = useState(false);
  const [scanning, setScanning] = useState(false);
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
      <ConnectionSetup
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

  return (
    <SafeAreaView style={styles.safe}>
      <StatusBar barStyle="dark-content" backgroundColor="#f6f3ec" />
      <View style={styles.header}>
        <View style={{ flex: 1, paddingRight: 12 }}>
          <Text style={styles.eyebrow}>OpenAgentFleet</Text>
          <Text numberOfLines={1} style={styles.title}>
            {screen === "chat"
              ? remote.bootstrap?.conversation.title || "Agent"
              : screen === "computer"
                ? "Computer"
                : screen === "routines"
                  ? "Routines"
                  : "This phone"}
          </Text>
        </View>
        <StatusPill state={remote.state} />
      </View>
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
      {!keyboardOpen ? (
        <View style={styles.tabs}>
          <Tab label="Chat" active={screen === "chat"} onPress={() => setScreen("chat")} />
          <Tab label="Computer" active={screen === "computer"} onPress={() => setScreen("computer")} />
          <Tab label="Routines" active={screen === "routines"} onPress={() => setScreen("routines")} />
          <Tab label="Device" active={screen === "settings"} onPress={() => setScreen("settings")} />
        </View>
      ) : null}
    </SafeAreaView>
  );
}

function ConnectionSetup(props: {
  pairingBundle: string;
  setPairingBundle: (value: string) => void;
  connecting: boolean;
  scanning: boolean;
  setScanning: (value: boolean) => void;
  error: string | null;
  onPair: () => void;
  onScanned: (value: string) => void;
}) {
  const [permission, requestPermission] = useCameraPermissions();
  const [pasteOpen, setPasteOpen] = useState(false);
  const scanned = useRef(false);

  const startScan = async () => {
    scanned.current = false;
    const current = permission?.granted ? permission : await requestPermission();
    if (!current.granted) {
      Alert.alert("Camera permission", "Allow camera access to scan the pairing QR, or paste the JSON instead.");
      setPasteOpen(true);
      return;
    }
    props.setScanning(true);
  };

  if (props.scanning) {
    return (
      <View style={styles.scanner}>
        <StatusBar barStyle="light-content" backgroundColor="#11110e" />
        <CameraView
          style={StyleSheet.absoluteFill}
          facing="back"
          barcodeScannerSettings={{ barcodeTypes: ["qr"] }}
          onBarcodeScanned={({ data }) => {
            if (scanned.current || props.connecting) return;
            scanned.current = true;
            props.setScanning(false);
            props.onScanned(data);
          }}
        />
        <View style={styles.scannerMask} pointerEvents="none">
          <View style={styles.scannerFrame} />
          <Text style={styles.scannerHint}>Point at the QR in the desktop app</Text>
        </View>
        <Pressable accessibilityRole="button" style={styles.scannerClose} onPress={() => props.setScanning(false)}>
          <Text style={styles.scannerCloseText}>Cancel</Text>
        </Pressable>
      </View>
    );
  }

  return (
    <SafeAreaView style={styles.safe}>
      <StatusBar barStyle="dark-content" backgroundColor="#f6f3ec" />
      <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === "ios" ? "padding" : undefined}>
        <ScrollView contentContainerStyle={styles.setup} keyboardShouldPersistTaps="handled">
          <Text style={styles.eyebrow}>OpenAgentFleet</Text>
          <Text style={styles.hero}>Connect this phone to your computer.</Text>
          <Text style={styles.lede}>
            Create a pairing QR in the desktop app, then scan it. The host stays private on your Tailnet — this phone is a remote, not a second runtime.
          </Text>
          {props.error ? <Text style={styles.error}>{props.error}</Text> : null}
          <Pressable
            accessibilityRole="button"
            disabled={props.connecting}
            android_ripple={{ color: "#ffffff33" }}
            style={[styles.primaryButton, props.connecting && styles.disabled]}
            onPress={() => void startScan()}
          >
            {props.connecting ? <ActivityIndicator color="#fff" /> : <Text style={styles.primaryText}>Scan pairing QR</Text>}
          </Pressable>
          <Pressable accessibilityRole="button" onPress={() => setPasteOpen((value) => !value)}>
            <Text style={styles.textLink}>{pasteOpen ? "Hide paste field" : "Paste JSON instead"}</Text>
          </Pressable>
          {pasteOpen ? (
            <>
              <TextInput
                autoCapitalize="none"
                autoCorrect={false}
                multiline
                placeholder='{"version":1,...}'
                placeholderTextColor="#88857e"
                style={[styles.input, styles.bundleInput]}
                value={props.pairingBundle}
                onChangeText={props.setPairingBundle}
              />
              <Pressable
                accessibilityRole="button"
                disabled={props.connecting || !props.pairingBundle.trim()}
                style={[styles.secondaryButton, styles.pairPaste, (props.connecting || !props.pairingBundle.trim()) && styles.disabled]}
                onPress={props.onPair}
              >
                <Text style={styles.secondaryText}>Pair with pasted bundle</Text>
              </Pressable>
            </>
          ) : null}
        </ScrollView>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
}

function ChatScreen(props: {
  conversations: Conversation[];
  activeID?: string;
  messages: { id: string; role: string; content: string; created_at: string }[];
  approvals: MobileApproval[];
  otherApprovalCount: number;
  activeRun?: MobileRun;
  canControl: boolean;
  composer: string;
  onChange: (value: string) => void;
  onSend: () => void;
  onSelectConversation: (id: string) => void;
  onResolve: (approval: MobileApproval, status: "approved" | "denied") => void;
  onStop: (run: MobileRun) => void;
  sending: boolean;
}) {
  const listRef = useRef<FlatList>(null);
  type Row =
    | { kind: "approval"; id: string; approval: MobileApproval }
    | { kind: "note"; id: string; text: string }
    | { kind: "message"; id: string; role: string; content: string };

  const rows: Row[] = [
    ...props.approvals.map((approval) => ({ kind: "approval" as const, id: approval.id, approval })),
    ...(props.otherApprovalCount > 0
      ? [{ kind: "note" as const, id: "other-approvals", text: `${props.otherApprovalCount} pending in other chats` }]
      : []),
    ...props.messages.map((message) => ({
      kind: "message" as const,
      id: message.id,
      role: message.role,
      content: message.content
    }))
  ];

  return (
    <View style={styles.content}>
      {props.conversations.length > 1 ? (
        <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={styles.conversationRow} keyboardShouldPersistTaps="handled">
          {props.conversations.map((conversation) => (
            <Pressable
              accessibilityRole="button"
              android_ripple={{ color: "#00000014" }}
              key={conversation.id}
              onPress={() => props.onSelectConversation(conversation.id)}
              style={[styles.conversationPill, conversation.id === props.activeID && styles.conversationPillActive]}
            >
              <Text numberOfLines={1} style={conversation.id === props.activeID ? styles.conversationTextActive : styles.conversationText}>
                {conversation.title || "Untitled"}
              </Text>
            </Pressable>
          ))}
        </ScrollView>
      ) : null}
      <FlatList
        ref={listRef}
        style={styles.messages}
        contentContainerStyle={styles.messageList}
        data={rows}
        keyExtractor={(item) => item.id}
        keyboardShouldPersistTaps="handled"
        keyboardDismissMode={Platform.OS === "ios" ? "interactive" : "on-drag"}
        onContentSizeChange={() => listRef.current?.scrollToEnd({ animated: false })}
        renderItem={({ item }) => {
          if (item.kind === "approval") {
            return (
              <View style={styles.approvalCard}>
                <Text style={styles.approvalTitle}>Needs approval</Text>
                <Text style={styles.subtle}>{item.approval.action}</Text>
                {props.canControl ? (
                  <View style={styles.approvalActions}>
                    <Pressable accessibilityRole="button" disabled={props.sending} style={[styles.allowButton, props.sending && styles.disabled]} onPress={() => props.onResolve(item.approval, "approved")}>
                      <Text style={styles.allowText}>Allow</Text>
                    </Pressable>
                    <Pressable accessibilityRole="button" disabled={props.sending} style={[styles.denyButton, props.sending && styles.disabled]} onPress={() => props.onResolve(item.approval, "denied")}>
                      <Text style={styles.denyText}>Deny</Text>
                    </Pressable>
                  </View>
                ) : (
                  <Text style={styles.subtle}>This device is read-only.</Text>
                )}
              </View>
            );
          }
          if (item.kind === "note") {
            return <Text style={styles.otherApprovals}>{item.text}</Text>;
          }
          return (
            <View style={[styles.message, item.role === "user" ? styles.userMessage : styles.agentMessage]}>
              <Text style={item.role === "user" ? styles.userMessageText : styles.agentMessageText}>{item.content}</Text>
            </View>
          );
        }}
      />
      {props.activeRun && props.canControl ? (
        <Pressable
          accessibilityRole="button"
          disabled={props.sending}
          style={[styles.stopButton, props.sending && styles.disabled]}
          onPress={() => {
            if (props.activeRun) props.onStop(props.activeRun);
          }}
        >
          <Text style={styles.stopText}>Stop run</Text>
        </Pressable>
      ) : null}
      <View style={styles.composer}>
        <TextInput
          multiline
          placeholder="Message your agent"
          placeholderTextColor="#8e8a82"
          style={styles.composerInput}
          value={props.composer}
          onChangeText={props.onChange}
          blurOnSubmit={false}
        />
        <Pressable
          accessibilityRole="button"
          disabled={props.sending || !props.composer.trim()}
          android_ripple={{ color: "#ffffff33" }}
          style={[styles.sendButton, (!props.composer.trim() || props.sending) && styles.disabled]}
          onPress={props.onSend}
        >
          <Text style={styles.sendText}>{props.sending ? "…" : "↑"}</Text>
        </Pressable>
      </View>
    </View>
  );
}

function RoutinesScreen(props: {
  routines: MobileRoutine[];
  error?: string;
  canControl: boolean;
  busy: boolean;
  onPause: (routine: MobileRoutine) => void;
  onEnable: (routine: MobileRoutine) => void;
  onRefresh: () => void;
}) {
  return (
    <ScrollView contentContainerStyle={styles.settings} keyboardShouldPersistTaps="handled">
      <View style={styles.computerTop}>
        <View style={{ flex: 1 }}>
          <Text style={styles.sectionTitle}>Scheduled work</Text>
          <Text style={styles.subtle}>{props.canControl ? "Pause or enable from this phone." : "Observer devices can list routines only."}</Text>
        </View>
        <Pressable accessibilityRole="button" style={styles.secondaryButton} onPress={props.onRefresh}>
          <Text style={styles.secondaryText}>Refresh</Text>
        </Pressable>
      </View>
      {props.error ? <Text style={styles.error}>{props.error}</Text> : null}
      {props.routines.length === 0 ? <Text style={styles.subtle}>No routines on this host yet.</Text> : null}
      {props.routines.map((routine) => (
        <View key={routine.id} style={styles.routineRow}>
          <Text style={styles.routineName}>{routine.name}</Text>
          <Text style={styles.routineMeta}>
            {routine.status} · {routine.kind}
            {routine.next_run_at ? ` · next ${routine.next_run_at}` : ""}
          </Text>
          {routine.attention_reason ? <Text style={styles.error}>{routine.attention_reason}</Text> : null}
          {props.canControl && routine.status === "enabled" ? (
            <Pressable accessibilityRole="button" disabled={props.busy} style={[styles.secondaryButton, styles.routineAction, props.busy && styles.disabled]} onPress={() => props.onPause(routine)}>
              <Text style={styles.secondaryText}>Pause</Text>
            </Pressable>
          ) : null}
          {props.canControl && (routine.status === "paused" || routine.status === "disabled") ? (
            <Pressable accessibilityRole="button" disabled={props.busy} style={[styles.secondaryButton, styles.routineAction, props.busy && styles.disabled]} onPress={() => props.onEnable(routine)}>
              <Text style={styles.secondaryText}>Enable</Text>
            </Pressable>
          ) : null}
        </View>
      ))}
    </ScrollView>
  );
}

function ComputerScreen(props: {
  computer?: ComputerStatus;
  frame?: string;
  frameError?: string;
  loading: boolean;
  controlHeld: boolean;
  controlBusy: boolean;
  onToggleControl: () => void;
  onClick: (x: number, y: number) => void;
  onRefresh: () => void;
}) {
  const [frameBounds, setFrameBounds] = useState({ width: 0, height: 0 });
  const ready = Boolean(props.computer?.running && props.computer.browser_ready && props.frame);
  const pressFrame = (event: GestureResponderEvent) => {
    if (!props.controlHeld || !ready || !event || !frameBounds.width || !frameBounds.height) return;
    const sourceWidth = props.computer?.viewport_width || 1280;
    const sourceHeight = props.computer?.viewport_height || 720;
    const scale = Math.min(frameBounds.width / sourceWidth, frameBounds.height / sourceHeight);
    const renderedWidth = sourceWidth * scale;
    const renderedHeight = sourceHeight * scale;
    const offsetX = (frameBounds.width - renderedWidth) / 2;
    const offsetY = (frameBounds.height - renderedHeight) / 2;
    const x = (event.nativeEvent.locationX - offsetX) / scale;
    const y = (event.nativeEvent.locationY - offsetY) / scale;
    if (x < 0 || y < 0 || x > sourceWidth || y > sourceHeight) return;
    props.onClick(x, y);
  };
  return (
    <ScrollView contentContainerStyle={styles.computerContent} keyboardShouldPersistTaps="handled">
      <View style={styles.computerTop}>
        <View style={{ flex: 1 }}>
          <Text style={styles.sectionTitle}>Agent Computer</Text>
          <Text style={styles.subtle}>{props.computer?.title || props.computer?.detail || "Waiting for the Agent Computer"}</Text>
        </View>
        <Pressable accessibilityRole="button" disabled={props.loading} style={[styles.secondaryButton, props.loading && styles.disabled]} onPress={props.onRefresh}>
          {props.loading ? <ActivityIndicator color="#3e4039" /> : <Text style={styles.secondaryText}>Refresh</Text>}
        </Pressable>
      </View>
      <Pressable
        accessibilityRole="imagebutton"
        accessibilityLabel={props.controlHeld ? "Click the remote desktop" : "Read-only Agent Computer frame"}
        disabled={!props.controlHeld || !ready || props.controlBusy}
        style={styles.frame}
        onLayout={(event) => setFrameBounds({ width: event.nativeEvent.layout.width, height: event.nativeEvent.layout.height })}
        onPress={pressFrame}
      >
        {ready ? (
          <Image source={{ uri: props.frame }} style={styles.frameImage} resizeMode="contain" />
        ) : (
          <View style={styles.emptyFrame}>
            <Text style={styles.emptyTitle}>{props.frameError ? "Frame unavailable" : "Computer not ready"}</Text>
            <Text style={styles.subtle}>{props.frameError || "Start the computer from the desktop app."}</Text>
          </View>
        )}
      </Pressable>
      <Text style={styles.frameCaption}>
        {props.controlHeld
          ? "Short click lease · passwords stay on the desktop"
          : "Watch-only until you take a short click lease"}
      </Text>
      <Pressable accessibilityRole="button" disabled={props.controlBusy} style={[styles.primaryButton, { marginTop: 12 }, props.controlBusy && styles.disabled]} onPress={props.onToggleControl}>
        <Text style={styles.primaryText}>{props.controlBusy ? "Working…" : props.controlHeld ? "Release control" : "Take control"}</Text>
      </Pressable>
    </ScrollView>
  );
}

function SettingsScreen(props: { profile: RemoteProfile; state: string; computer?: ComputerStatus; onDisconnect: () => void }) {
  return (
    <ScrollView contentContainerStyle={styles.settings}>
      <Text style={styles.sectionTitle}>This device</Text>
      <Setting label="Host" value={props.profile.baseUrl} />
      <Setting label="Host identity" value={props.profile.hostId} />
      <Setting label="Device" value={props.profile.device.name} />
      <Setting label="Connection" value={props.state} />
      <Setting label="Agent Computer" value={props.computer?.running ? "Running" : "Not running"} />
      <Pressable accessibilityRole="button" style={styles.destructiveButton} onPress={props.onDisconnect}>
        <Text style={styles.destructiveText}>Disconnect</Text>
      </Pressable>
    </ScrollView>
  );
}

function Setting(props: { label: string; value: string }) {
  return (
    <View style={styles.setting}>
      <Text style={styles.settingLabel}>{props.label}</Text>
      <Text selectable style={styles.settingValue}>
        {props.value}
      </Text>
    </View>
  );
}

function Tab(props: { label: string; active: boolean; onPress: () => void }) {
  return (
    <Pressable accessibilityRole="tab" accessibilityState={{ selected: props.active }} android_ripple={{ color: "#00000014" }} style={styles.tab} onPress={props.onPress}>
      <Text style={props.active ? styles.tabActive : styles.tabText}>{props.label}</Text>
      {props.active ? <View style={styles.tabIndicator} /> : <View style={styles.tabIndicatorOff} />}
    </Pressable>
  );
}

function StatusPill(props: { state: string }) {
  return (
    <View style={[styles.status, props.state === "connected" ? styles.statusGood : props.state === "degraded" ? styles.statusWarn : styles.statusNeutral]}>
      <View style={styles.statusDot} />
      <Text style={styles.statusText}>{props.state}</Text>
    </View>
  );
}

const styles = StyleSheet.create({
  safe: { flex: 1, backgroundColor: "#f6f3ec", paddingTop: Platform.OS === "android" ? StatusBar.currentHeight || 0 : 0 },
  header: {
    paddingHorizontal: 20,
    paddingTop: 12,
    paddingBottom: 12,
    borderBottomWidth: StyleSheet.hairlineWidth,
    borderColor: "#e2ddd4",
    flexDirection: "row",
    justifyContent: "space-between",
    alignItems: "center"
  },
  eyebrow: { color: "#6f6a62", fontSize: 12, fontWeight: "700", letterSpacing: 0.2 },
  title: { color: "#1b1813", fontSize: 22, fontWeight: "700", marginTop: 2 },
  hero: { color: "#1b1813", fontSize: 34, lineHeight: 40, fontWeight: "700", letterSpacing: -0.8, marginTop: 12 },
  lede: { color: "#58544c", fontSize: 16, lineHeight: 23, marginTop: 14 },
  setup: { padding: 24, flexGrow: 1, justifyContent: "center" },
  input: { backgroundColor: "#fffefa", color: "#20211d", borderColor: "#d8d2c7", borderWidth: 1, borderRadius: 14, minHeight: 54, paddingHorizontal: 15, fontSize: 15, marginTop: 16 },
  bundleInput: { minHeight: 128, paddingVertical: 14, textAlignVertical: "top" },
  primaryButton: { minHeight: 52, borderRadius: 14, backgroundColor: "#1b1813", alignItems: "center", justifyContent: "center", marginTop: 22 },
  primaryText: { color: "#f6f3ec", fontSize: 16, fontWeight: "700" },
  disabled: { opacity: 0.45 },
  error: { color: "#a42920", marginTop: 12, lineHeight: 20 },
  content: { flex: 1 },
  conversationRow: { paddingHorizontal: 16, paddingVertical: 10, gap: 8 },
  conversationPill: { paddingHorizontal: 12, paddingVertical: 8, borderRadius: 999, maxWidth: 160, backgroundColor: "#ece8df" },
  conversationPillActive: { backgroundColor: "#1b1813" },
  conversationText: { color: "#5b584f", fontSize: 13, fontWeight: "600" },
  conversationTextActive: { color: "#f6f3ec", fontSize: 13, fontWeight: "700" },
  messages: { flex: 1 },
  messageList: { paddingHorizontal: 16, paddingBottom: 12, gap: 10 },
  message: { maxWidth: "86%", paddingHorizontal: 14, paddingVertical: 10, borderRadius: 18 },
  userMessage: { backgroundColor: "#1b1813", alignSelf: "flex-end", borderBottomRightRadius: 5 },
  agentMessage: { backgroundColor: "#ece9e2", alignSelf: "flex-start", borderBottomLeftRadius: 5 },
  userMessageText: { color: "#f6f3ec", fontSize: 16, lineHeight: 22 },
  agentMessageText: { color: "#292a25", fontSize: 16, lineHeight: 22 },
  composer: {
    flexDirection: "row",
    gap: 8,
    paddingHorizontal: 14,
    paddingTop: 10,
    paddingBottom: Platform.OS === "android" ? 10 : 12,
    borderTopWidth: StyleSheet.hairlineWidth,
    borderColor: "#e2ddd4",
    backgroundColor: "#f6f3ec",
    alignItems: "flex-end"
  },
  composerInput: { flex: 1, minHeight: 44, maxHeight: 120, backgroundColor: "#fff", borderRadius: 22, paddingHorizontal: 16, paddingVertical: 11, color: "#1b1813", fontSize: 16 },
  sendButton: { width: 44, height: 44, borderRadius: 22, alignItems: "center", justifyContent: "center", backgroundColor: "#1b1813" },
  sendText: { color: "#f6f3ec", fontSize: 20, fontWeight: "700", marginTop: -1 },
  status: { flexDirection: "row", alignItems: "center", gap: 6, borderRadius: 999, paddingHorizontal: 10, paddingVertical: 6 },
  statusGood: { backgroundColor: "#dcebd9" },
  statusWarn: { backgroundColor: "#f2e3ba" },
  statusNeutral: { backgroundColor: "#e7e4de" },
  statusDot: { width: 7, height: 7, backgroundColor: "#35654d", borderRadius: 4 },
  statusText: { color: "#3d4038", fontWeight: "700", fontSize: 12 },
  computerContent: { padding: 16 },
  computerTop: { flexDirection: "row", alignItems: "center", justifyContent: "space-between", gap: 12 },
  sectionTitle: { fontSize: 20, color: "#1b1813", fontWeight: "700" },
  subtle: { color: "#716e66", marginTop: 4, lineHeight: 19 },
  secondaryButton: { paddingHorizontal: 13, paddingVertical: 9, borderRadius: 10, backgroundColor: "#e8e5dc" },
  secondaryText: { color: "#3e4039", fontWeight: "700" },
  frame: { marginTop: 16, height: 320, borderRadius: 16, overflow: "hidden", backgroundColor: "#1b1813" },
  frameImage: { width: "100%", height: "100%" },
  emptyFrame: { flex: 1, justifyContent: "center", alignItems: "center", padding: 20 },
  emptyTitle: { color: "#fff", fontWeight: "700", fontSize: 18 },
  frameCaption: { color: "#827e74", fontSize: 12, marginTop: 8 },
  settings: { padding: 20, gap: 8 },
  setting: { paddingVertical: 14, borderBottomWidth: StyleSheet.hairlineWidth, borderColor: "#e2ddd4" },
  settingLabel: { color: "#77736a", fontSize: 12, fontWeight: "700" },
  settingValue: { color: "#252620", fontSize: 16, marginTop: 4 },
  destructiveButton: { borderWidth: 1, borderColor: "#bb4a42", borderRadius: 14, minHeight: 50, alignItems: "center", justifyContent: "center", marginTop: 16 },
  destructiveText: { color: "#a8322a", fontWeight: "800" },
  tabs: {
    flexDirection: "row",
    borderTopWidth: StyleSheet.hairlineWidth,
    borderColor: "#e2ddd4",
    backgroundColor: "#f6f3ec",
    paddingTop: 4,
    paddingBottom: Platform.OS === "android" ? 10 : 6
  },
  tab: { flex: 1, minHeight: 44, justifyContent: "center", alignItems: "center" },
  tabText: { color: "#817d74", fontSize: 12, fontWeight: "700" },
  tabActive: { color: "#1b1813", fontSize: 12, fontWeight: "800" },
  tabIndicator: { marginTop: 6, width: 18, height: 3, borderRadius: 2, backgroundColor: "#1b1813" },
  tabIndicatorOff: { marginTop: 6, width: 18, height: 3 },
  approvalCard: { backgroundColor: "#efe6d2", borderRadius: 14, padding: 14, gap: 6 },
  approvalTitle: { color: "#5a3b12", fontWeight: "800", fontSize: 12, letterSpacing: 0.4 },
  approvalActions: { flexDirection: "row", gap: 8, marginTop: 6 },
  allowButton: { backgroundColor: "#1b1813", borderRadius: 10, paddingHorizontal: 14, paddingVertical: 8 },
  allowText: { color: "#f6f3ec", fontWeight: "800" },
  denyButton: { backgroundColor: "#f3d7d3", borderRadius: 10, paddingHorizontal: 14, paddingVertical: 8 },
  denyText: { color: "#a8322a", fontWeight: "800" },
  otherApprovals: { color: "#7a5a20", fontSize: 13, fontWeight: "700" },
  stopButton: { marginHorizontal: 16, marginBottom: 8, minHeight: 42, borderRadius: 12, backgroundColor: "#bb4a42", alignItems: "center", justifyContent: "center" },
  stopText: { color: "#fff", fontWeight: "800" },
  routineRow: { paddingVertical: 14, borderBottomWidth: StyleSheet.hairlineWidth, borderColor: "#e2ddd4" },
  routineName: { color: "#1b1813", fontSize: 17, fontWeight: "700" },
  routineMeta: { color: "#716e66", marginTop: 4, lineHeight: 19 },
  routineAction: { alignSelf: "flex-start", marginTop: 10 },
  textLink: { color: "#1b1813", fontWeight: "700", fontSize: 15, marginTop: 18, textDecorationLine: "underline" },
  pairPaste: { marginTop: 12, alignSelf: "flex-start" },
  scanner: { flex: 1, backgroundColor: "#11110e" },
  scannerMask: { position: "absolute", top: 0, right: 0, bottom: 0, left: 0, alignItems: "center", justifyContent: "center" },
  scannerFrame: { width: 240, height: 240, borderRadius: 24, borderWidth: 3, borderColor: "#f6f3ec" },
  scannerHint: { color: "#f6f3ec", marginTop: 18, fontSize: 15, fontWeight: "600" },
  scannerClose: { position: "absolute", bottom: 36, alignSelf: "center", paddingHorizontal: 22, paddingVertical: 12, borderRadius: 999, backgroundColor: "#f6f3ec" },
  scannerCloseText: { color: "#1b1813", fontWeight: "800" }
});

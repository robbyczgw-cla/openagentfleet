import { useCallback, useEffect, useRef, useState } from "react";
import {
  ActivityIndicator,
  Alert,
  type GestureResponderEvent,
  Image,
  Pressable,
  Platform,
  SafeAreaView,
  ScrollView,
  StatusBar,
  StyleSheet,
  Text,
  TextInput,
  View
} from "react-native";

import { defaultDeviceName, parsePairingBundle } from "./src/api/pairing";
import type { ComputerStatus, Conversation, RemoteProfile } from "./src/api/types";
import { useRemoteSession } from "./src/hooks/useRemoteSession";

type Screen = "chat" | "computer" | "settings";

export default function App() {
  const remote = useRemoteSession();
  const [screen, setScreen] = useState<Screen>("chat");
  const [composer, setComposer] = useState("");
  const [pairingBundle, setPairingBundle] = useState("");
  const [connecting, setConnecting] = useState(false);
  const [sending, setSending] = useState(false);
  const [frame, setFrame] = useState<string | undefined>(undefined);
  const [frameLoading, setFrameLoading] = useState(false);
  const [frameError, setFrameError] = useState<string | undefined>(undefined);
  const [computerControlHeld, setComputerControlHeld] = useState(false);
  const [computerControlBusy, setComputerControlBusy] = useState(false);
  const autoFrameClient = useRef<typeof remote.client>(null);

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

  const pair = async () => {
    setConnecting(true);
    try {
      const bundle = parsePairingBundle(pairingBundle);
      await remote.pair(bundle, defaultDeviceName(Platform.OS), Platform.OS);
      setScreen("chat");
    } catch (error) {
      Alert.alert("Pairing failed", error instanceof Error ? error.message : "Create a fresh pairing bundle on your Mac and try again.");
    } finally {
      setPairingBundle("");
      setConnecting(false);
    }
  };

  const send = async () => {
    if (!composer.trim()) return;
    setSending(true);
    try {
      await remote.send(composer);
      setComposer("");
    } catch (error) {
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
      Alert.alert("Computer control unavailable", error instanceof Error ? error.message : "Take control from the Mac and try again.");
    } finally {
      setComputerControlBusy(false);
    }
  };

  const clickComputer = async (x: number, y: number) => {
    if (!remote.client || !computerControlHeld || computerControlBusy) return;
    setComputerControlBusy(true);
    try {
      // The mobile preview is the browser frame; keep its coordinate space
      // paired with the browser action endpoint. Desktop control remains a
      // separate Mac-local surface until mobile can stream a desktop frame.
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
    return <ConnectionSetup pairingBundle={pairingBundle} setPairingBundle={setPairingBundle} connecting={connecting} error={remote.error} onPair={pair} />;
  }

  return (
    <SafeAreaView style={styles.safe}>
      <StatusBar barStyle="dark-content" />
      <View style={styles.header}>
        <View>
          <Text style={styles.eyebrow}>OPENAGENTFLEET</Text>
          <Text style={styles.title}>{screen === "chat" ? remote.bootstrap?.conversation.title || "Remote agent" : screen === "computer" ? "Agent Computer" : "Remote Mac"}</Text>
        </View>
        <StatusPill state={remote.state} />
      </View>
      {screen === "chat" && <ChatScreen conversations={remote.bootstrap?.conversations || []} activeID={remote.bootstrap?.conversation.id} messages={remote.bootstrap?.messages || []} composer={composer} onChange={setComposer} onSend={send} onSelectConversation={(id) => void remote.selectConversation(id).catch((error) => Alert.alert("Could not open conversation", error instanceof Error ? error.message : "Try again."))} sending={sending} />}
      {screen === "computer" && <ComputerScreen computer={computer} frame={frame} frameError={frameError} loading={frameLoading} controlHeld={computerControlHeld} controlBusy={computerControlBusy} onToggleControl={() => void toggleComputerControl()} onClick={clickComputer} onRefresh={() => void refreshFrame()} />}
      {screen === "settings" && <SettingsScreen profile={remote.profile} state={remote.state} computer={computer} onDisconnect={() => void remote.disconnect()} />}
      <View style={styles.tabs}>
        <Tab label="Chat" active={screen === "chat"} onPress={() => setScreen("chat")} />
        <Tab label="Computer" active={screen === "computer"} onPress={() => setScreen("computer")} />
        <Tab label="Settings" active={screen === "settings"} onPress={() => setScreen("settings")} />
      </View>
    </SafeAreaView>
  );
}

function ConnectionSetup(props: { pairingBundle: string; setPairingBundle: (value: string) => void; connecting: boolean; error: string | null; onPair: () => void }) {
  return (
    <SafeAreaView style={styles.safe}>
      <StatusBar barStyle="dark-content" />
      <ScrollView contentContainerStyle={styles.setup} keyboardShouldPersistTaps="handled">
        <Text style={styles.eyebrow}>OPENAGENTFLEET / REMOTE</Text>
        <Text style={styles.hero}>Pair this phone with your Mac.</Text>
        <Text style={styles.lede}>Create a short-lived pairing bundle in the trusted Mac app, then paste the complete bundle here. The app only accepts private HTTPS Tailnet hosts.</Text>
        <Text style={styles.label}>Pairing bundle</Text>
        <TextInput autoCapitalize="none" autoCorrect={false} multiline placeholder='Paste {"version":1,...} from your Mac' placeholderTextColor="#88857e" style={[styles.input, styles.bundleInput]} value={props.pairingBundle} onChangeText={props.setPairingBundle} />
        {props.error ? <Text style={styles.error}>{props.error}</Text> : null}
        <Pressable accessibilityRole="button" disabled={props.connecting || !props.pairingBundle.trim()} style={[styles.primaryButton, (props.connecting || !props.pairingBundle.trim()) && styles.disabled]} onPress={props.onPair}>
          {props.connecting ? <ActivityIndicator color="#fff" /> : <Text style={styles.primaryText}>Pair this device</Text>}
        </Pressable>
        <View style={styles.notice}><Text style={styles.noticeTitle}>Alpha security boundary</Text><Text style={styles.noticeBody}>Pairing exchanges the one-time bundle for a device-specific Bearer credential in iOS Keychain / Android Keystore storage. Controller and owner devices can request a short computer-control lease; observer devices remain read-only.</Text></View>
      </ScrollView>
    </SafeAreaView>
  );
}

function ChatScreen(props: { conversations: Conversation[]; activeID?: string; messages: { id: string; role: string; content: string; created_at: string }[]; composer: string; onChange: (value: string) => void; onSend: () => void; onSelectConversation: (id: string) => void; sending: boolean }) {
  return <View style={styles.content}><ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={styles.conversationRow}>{props.conversations.map((conversation) => <Pressable accessibilityRole="button" key={conversation.id} onPress={() => props.onSelectConversation(conversation.id)} style={[styles.conversationPill, conversation.id === props.activeID && styles.conversationPillActive]}><Text numberOfLines={1} style={conversation.id === props.activeID ? styles.conversationTextActive : styles.conversationText}>{conversation.title || "Untitled"}</Text></Pressable>)}</ScrollView><ScrollView style={styles.messages} contentContainerStyle={styles.messageList}>{props.messages.map((message) => <View key={message.id} style={[styles.message, message.role === "user" ? styles.userMessage : styles.agentMessage]}><Text style={message.role === "user" ? styles.userMessageText : styles.agentMessageText}>{message.content}</Text></View>)}</ScrollView><View style={styles.composer}><TextInput multiline placeholder="Message your remote agent" placeholderTextColor="#8e8a82" style={styles.composerInput} value={props.composer} onChangeText={props.onChange} /><Pressable accessibilityRole="button" disabled={props.sending || !props.composer.trim()} style={[styles.sendButton, (!props.composer.trim() || props.sending) && styles.disabled]} onPress={props.onSend}><Text style={styles.sendText}>{props.sending ? "…" : "↑"}</Text></Pressable></View></View>;
}

function ComputerScreen(props: { computer?: ComputerStatus; frame?: string; frameError?: string; loading: boolean; controlHeld: boolean; controlBusy: boolean; onToggleControl: () => void; onClick: (x: number, y: number) => void; onRefresh: () => void }) {
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
  return <ScrollView contentContainerStyle={styles.computerContent}><View style={styles.computerTop}><View><Text style={styles.sectionTitle}>Builder’s Computer</Text><Text style={styles.subtle}>{props.computer?.title || props.computer?.detail || "Waiting for the Agent Computer"}</Text></View><Pressable accessibilityRole="button" disabled={props.loading} style={[styles.secondaryButton, props.loading && styles.disabled]} onPress={props.onRefresh}>{props.loading ? <ActivityIndicator color="#3e4039" /> : <Text style={styles.secondaryText}>Refresh</Text>}</Pressable></View><Pressable accessibilityRole="imagebutton" accessibilityLabel={props.controlHeld ? "Click the remote desktop" : "Read-only Agent Computer frame"} disabled={!props.controlHeld || !ready || props.controlBusy} style={styles.frame} onLayout={(event) => setFrameBounds({ width: event.nativeEvent.layout.width, height: event.nativeEvent.layout.height })} onPress={pressFrame}>{ready ? <Image source={{ uri: props.frame }} style={styles.frameImage} resizeMode="contain" /> : <View style={styles.emptyFrame}><Text style={styles.emptyTitle}>{props.frameError ? "Frame unavailable" : "Computer not ready"}</Text><Text style={styles.subtle}>{props.frameError || "Start an agent run or ensure the computer from the Mac app."}</Text></View>}</Pressable><Text style={styles.frameCaption}>{props.controlHeld ? "Short control lease · tap the frame to click · passwords and OTPs stay on the trusted Mac" : "Authenticated read-only frame · request a short control lease to click"}</Text><Pressable accessibilityRole="button" disabled={props.controlBusy} style={[styles.primaryButton, props.controlBusy && styles.disabled]} onPress={props.onToggleControl}><Text style={styles.primaryText}>{props.controlBusy ? "Working…" : props.controlHeld ? "Release computer control" : "Take computer control"}</Text></Pressable><View style={styles.notice}><Text style={styles.noticeTitle}>{props.controlHeld ? "Controller lease active" : "Remote computer safety"}</Text><Text style={styles.noticeBody}>{props.controlHeld ? "This device can click the isolated desktop until the short lease expires or you release it. Use the trusted Mac app for passwords, 2FA, CAPTCHA, payment approvals, and keyboard input." : "Only controller or owner paired devices can request control. Observer devices remain read-only, and the Mac can revoke the lease at any time."}</Text></View></ScrollView>;
}

function SettingsScreen(props: { profile: RemoteProfile; state: string; computer?: ComputerStatus; onDisconnect: () => void }) {
  return <ScrollView contentContainerStyle={styles.settings}><Text style={styles.sectionTitle}>Paired device</Text><Setting label="Tailnet endpoint" value={props.profile.baseUrl} /><Setting label="Mac identity" value={props.profile.hostId} /><Setting label="This device" value={props.profile.device.name} /><Setting label="API state" value={props.state} /><Setting label="Agent Computer" value={props.computer?.running ? "Running" : "Not running"} /><View style={styles.notice}><Text style={styles.noticeTitle}>Alpha security boundary</Text><Text style={styles.noticeBody}>This phone stores its paired Bearer credential only in the iOS Keychain / Android Keystore abstraction. It never stores Agent Computer passwords, OTPs, or desktop input. Disconnect removes the local credential even if your Mac is offline.</Text></View><Pressable accessibilityRole="button" style={styles.destructiveButton} onPress={props.onDisconnect}><Text style={styles.destructiveText}>Disconnect this device</Text></Pressable></ScrollView>;
}

function Setting(props: { label: string; value: string }) { return <View style={styles.setting}><Text style={styles.settingLabel}>{props.label}</Text><Text selectable style={styles.settingValue}>{props.value}</Text></View>; }
function Tab(props: { label: string; active: boolean; onPress: () => void }) { return <Pressable accessibilityRole="tab" accessibilityState={{ selected: props.active }} style={styles.tab} onPress={props.onPress}><Text style={props.active ? styles.tabActive : styles.tabText}>{props.label}</Text></Pressable>; }
function StatusPill(props: { state: string }) { return <View style={[styles.status, props.state === "connected" ? styles.statusGood : props.state === "degraded" ? styles.statusWarn : styles.statusNeutral]}><View style={styles.statusDot} /><Text style={styles.statusText}>{props.state}</Text></View>; }

const styles = StyleSheet.create({
  safe: { flex: 1, backgroundColor: "#f7f5f0" }, header: { paddingHorizontal: 22, paddingTop: 18, paddingBottom: 14, borderBottomWidth: StyleSheet.hairlineWidth, borderColor: "#d9d4ca", flexDirection: "row", justifyContent: "space-between", alignItems: "center" }, eyebrow: { color: "#817b70", fontSize: 11, fontWeight: "800", letterSpacing: 1.2 }, title: { color: "#1e201b", fontSize: 25, fontWeight: "700", marginTop: 3 }, hero: { color: "#1e201b", fontSize: 42, lineHeight: 47, fontWeight: "700", letterSpacing: -1.3, marginTop: 15 }, lede: { color: "#58564f", fontSize: 16, lineHeight: 23, marginTop: 20 }, setup: { padding: 26, flexGrow: 1, justifyContent: "center" }, label: { color: "#34342e", fontSize: 13, fontWeight: "700", marginTop: 24, marginBottom: 8 }, input: { backgroundColor: "#fffefa", color: "#20211d", borderColor: "#d8d2c7", borderWidth: 1, borderRadius: 14, minHeight: 54, paddingHorizontal: 15, fontSize: 15 }, bundleInput: { minHeight: 148, paddingVertical: 14, textAlignVertical: "top" }, primaryButton: { minHeight: 52, borderRadius: 15, backgroundColor: "#1d3f35", alignItems: "center", justifyContent: "center", marginTop: 23 }, primaryText: { color: "#fff", fontSize: 16, fontWeight: "800" }, disabled: { opacity: 0.48 }, notice: { marginTop: 22, padding: 16, backgroundColor: "#ece8dd", borderRadius: 15 }, noticeTitle: { color: "#31312b", fontWeight: "800", fontSize: 14 }, noticeBody: { color: "#5b584f", lineHeight: 20, marginTop: 5 }, error: { color: "#a42920", marginTop: 12, lineHeight: 20 }, content: { flex: 1 }, conversationRow: { paddingHorizontal: 17, paddingVertical: 13, gap: 8 }, conversationPill: { paddingHorizontal: 13, paddingVertical: 9, borderRadius: 12, maxWidth: 150, backgroundColor: "#ece8df" }, conversationPillActive: { backgroundColor: "#d5e0d0" }, conversationText: { color: "#5b584f", fontSize: 13 }, conversationTextActive: { color: "#1e3d34", fontSize: 13, fontWeight: "700" }, messages: { flex: 1 }, messageList: { paddingHorizontal: 18, paddingBottom: 18, gap: 10 }, message: { maxWidth: "87%", paddingHorizontal: 14, paddingVertical: 11, borderRadius: 16 }, userMessage: { backgroundColor: "#1d3f35", alignSelf: "flex-end", borderBottomRightRadius: 4 }, agentMessage: { backgroundColor: "#ece9e2", alignSelf: "flex-start", borderBottomLeftRadius: 4 }, userMessageText: { color: "#fff", fontSize: 16, lineHeight: 22 }, agentMessageText: { color: "#292a25", fontSize: 16, lineHeight: 22 }, composer: { flexDirection: "row", gap: 9, paddingHorizontal: 16, paddingVertical: 12, borderTopWidth: StyleSheet.hairlineWidth, borderColor: "#d9d4ca", backgroundColor: "#fbfaf6", alignItems: "flex-end" }, composerInput: { flex: 1, minHeight: 45, maxHeight: 110, backgroundColor: "#f0eee8", borderRadius: 15, paddingHorizontal: 14, paddingVertical: 11, color: "#1e201b", fontSize: 16 }, sendButton: { width: 45, height: 45, borderRadius: 23, alignItems: "center", justifyContent: "center", backgroundColor: "#1d3f35" }, sendText: { color: "#fff", fontSize: 23, fontWeight: "700", marginTop: -2 }, status: { flexDirection: "row", alignItems: "center", gap: 6, borderRadius: 16, paddingHorizontal: 10, paddingVertical: 7 }, statusGood: { backgroundColor: "#dcebd9" }, statusWarn: { backgroundColor: "#f2e3ba" }, statusNeutral: { backgroundColor: "#e7e4de" }, statusDot: { width: 7, height: 7, backgroundColor: "#35654d", borderRadius: 4 }, statusText: { color: "#3d4038", fontWeight: "700", fontSize: 12 }, computerContent: { padding: 18 }, computerTop: { flexDirection: "row", alignItems: "center", justifyContent: "space-between", gap: 12 }, sectionTitle: { fontSize: 21, color: "#20211d", fontWeight: "700" }, subtle: { color: "#716e66", marginTop: 4, lineHeight: 19 }, secondaryButton: { paddingHorizontal: 13, paddingVertical: 9, borderRadius: 10, backgroundColor: "#e8e5dc" }, secondaryText: { color: "#3e4039", fontWeight: "700" }, frame: { marginTop: 18, height: 280, borderRadius: 16, overflow: "hidden", backgroundColor: "#292b27", borderWidth: 1, borderColor: "#cbc6bc" }, frameImage: { width: "100%", height: "100%" }, emptyFrame: { flex: 1, justifyContent: "center", alignItems: "center", padding: 20 }, emptyTitle: { color: "#fff", fontWeight: "700", fontSize: 18 }, frameCaption: { color: "#827e74", fontSize: 12, marginTop: 8 }, footnote: { color: "#635f57", lineHeight: 20, marginTop: 14 }, settings: { padding: 20, gap: 10 }, setting: { paddingVertical: 15, borderBottomWidth: StyleSheet.hairlineWidth, borderColor: "#d9d4ca" }, settingLabel: { color: "#77736a", fontSize: 12, fontWeight: "700", textTransform: "uppercase", letterSpacing: 0.6 }, settingValue: { color: "#252620", fontSize: 16, marginTop: 5 }, destructiveButton: { borderWidth: 1, borderColor: "#bb4a42", borderRadius: 14, minHeight: 50, alignItems: "center", justifyContent: "center", marginTop: 12 }, destructiveText: { color: "#a8322a", fontWeight: "800" }, tabs: { flexDirection: "row", borderTopWidth: StyleSheet.hairlineWidth, borderColor: "#d9d4ca", backgroundColor: "#fbfaf6", paddingVertical: 7 }, tab: { flex: 1, minHeight: 41, justifyContent: "center", alignItems: "center" }, tabText: { color: "#817d74", fontSize: 13, fontWeight: "700" }, tabActive: { color: "#1d3f35", fontSize: 13, fontWeight: "900" }
});

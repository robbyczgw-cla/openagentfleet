import { useMemo, useRef } from "react";
import {
  FlatList,
  Platform,
  Pressable,
  RefreshControl,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  View
} from "react-native";

import type { ChatMessage, Conversation, MobileApproval, MobileRun } from "../api/types";
import { type ThemeColors, useTheme } from "../theme";

export function ChatScreen(props: {
  conversations: Conversation[];
  activeID?: string;
  messages: ChatMessage[];
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
  onRefresh: () => void;
  refreshing: boolean;
  sending: boolean;
}) {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
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
              android_ripple={{ color: theme.ripple }}
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
        contentContainerStyle={[styles.messageList, rows.length === 0 && styles.messageListEmpty]}
        data={rows}
        keyExtractor={(item) => item.id}
        keyboardShouldPersistTaps="handled"
        keyboardDismissMode={Platform.OS === "ios" ? "interactive" : "on-drag"}
        onContentSizeChange={() => listRef.current?.scrollToEnd({ animated: false })}
        refreshControl={
          <RefreshControl
            refreshing={props.refreshing}
            onRefresh={props.onRefresh}
            tintColor={theme.textMuted}
            colors={[theme.text]}
            progressBackgroundColor={theme.surface}
          />
        }
        ListEmptyComponent={
          <View style={styles.empty}>
            <Text style={styles.emptyTitle}>No messages yet</Text>
            <Text style={styles.emptyCopy}>Send one to your agent.</Text>
          </View>
        }
        renderItem={({ item }) => {
          if (item.kind === "approval") {
            return (
              <View style={styles.approvalCard}>
                <Text style={styles.approvalTitle}>Needs approval</Text>
                <Text style={styles.subtle}>{item.approval.action}</Text>
                {props.canControl ? (
                  <View style={styles.approvalActions}>
                    <Pressable
                      accessibilityRole="button"
                      disabled={props.sending}
                      android_ripple={{ color: theme.rippleOnPrimary }}
                      style={[styles.allowButton, props.sending && styles.disabled]}
                      onPress={() => props.onResolve(item.approval, "approved")}
                    >
                      <Text style={styles.allowText}>Allow</Text>
                    </Pressable>
                    <Pressable
                      accessibilityRole="button"
                      disabled={props.sending}
                      android_ripple={{ color: theme.ripple }}
                      style={[styles.denyButton, props.sending && styles.disabled]}
                      onPress={() => props.onResolve(item.approval, "denied")}
                    >
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
          android_ripple={{ color: theme.rippleOnPrimary }}
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
          placeholderTextColor={theme.placeholder}
          style={styles.composerInput}
          value={props.composer}
          onChangeText={props.onChange}
          blurOnSubmit={false}
        />
        <Pressable
          accessibilityRole="button"
          disabled={props.sending || !props.composer.trim()}
          android_ripple={{ color: theme.rippleOnPrimary }}
          style={[styles.sendButton, (!props.composer.trim() || props.sending) && styles.disabled]}
          onPress={props.onSend}
        >
          <Text style={styles.sendText}>{props.sending ? "…" : "↑"}</Text>
        </Pressable>
      </View>
    </View>
  );
}

function createStyles(theme: ThemeColors) {
  return StyleSheet.create({
    content: { flex: 1 },
    conversationRow: { paddingHorizontal: 16, paddingVertical: 10, gap: 8 },
    conversationPill: { paddingHorizontal: 12, paddingVertical: 8, borderRadius: 999, maxWidth: 160, backgroundColor: theme.surfaceMuted, overflow: "hidden" },
    conversationPillActive: { backgroundColor: theme.primary },
    conversationText: { color: theme.textSubtle, fontSize: 13, fontWeight: "600" },
    conversationTextActive: { color: theme.primaryText, fontSize: 13, fontWeight: "700" },
    messages: { flex: 1 },
    messageList: { paddingHorizontal: 16, paddingBottom: 12, gap: 10 },
    messageListEmpty: { flexGrow: 1, justifyContent: "center" },
    empty: { alignItems: "center", paddingHorizontal: 32, paddingVertical: 24 },
    emptyTitle: { color: theme.text, fontSize: 18, fontWeight: "700" },
    emptyCopy: { color: theme.textSubtle, fontSize: 15, marginTop: 6 },
    message: { maxWidth: "86%", paddingHorizontal: 14, paddingVertical: 10, borderRadius: 18 },
    userMessage: { backgroundColor: theme.primary, alignSelf: "flex-end", borderBottomRightRadius: 5 },
    agentMessage: { backgroundColor: theme.surfaceMuted, alignSelf: "flex-start", borderBottomLeftRadius: 5 },
    userMessageText: { color: theme.primaryText, fontSize: 16, lineHeight: 22 },
    agentMessageText: { color: theme.text, fontSize: 16, lineHeight: 22 },
    composer: {
      flexDirection: "row",
      gap: 8,
      paddingHorizontal: 14,
      paddingTop: 10,
      paddingBottom: Platform.OS === "android" ? 10 : 12,
      borderTopWidth: StyleSheet.hairlineWidth,
      borderColor: theme.border,
      backgroundColor: theme.composerBg,
      alignItems: "flex-end"
    },
    composerInput: {
      flex: 1,
      minHeight: 44,
      maxHeight: 120,
      backgroundColor: theme.input,
      borderRadius: 22,
      paddingHorizontal: 16,
      paddingVertical: 11,
      color: theme.text,
      fontSize: 16
    },
    sendButton: { width: 44, height: 44, borderRadius: 22, alignItems: "center", justifyContent: "center", backgroundColor: theme.primary, overflow: "hidden" },
    sendText: { color: theme.primaryText, fontSize: 20, fontWeight: "700", marginTop: -1 },
    subtle: { color: theme.textSubtle, marginTop: 4, lineHeight: 19 },
    disabled: { opacity: 0.45 },
    approvalCard: { backgroundColor: theme.approvalBg, borderRadius: 14, padding: 14, gap: 6 },
    approvalTitle: { color: theme.approvalTitle, fontWeight: "800", fontSize: 12, letterSpacing: 0.4 },
    approvalActions: { flexDirection: "row", gap: 8, marginTop: 6 },
    allowButton: { backgroundColor: theme.primary, borderRadius: 10, paddingHorizontal: 14, paddingVertical: 8, overflow: "hidden" },
    allowText: { color: theme.primaryText, fontWeight: "800" },
    denyButton: { backgroundColor: theme.dangerFill, borderRadius: 10, paddingHorizontal: 14, paddingVertical: 8, overflow: "hidden" },
    denyText: { color: theme.dangerText, fontWeight: "800" },
    otherApprovals: { color: theme.otherApprovals, fontSize: 13, fontWeight: "700" },
    stopButton: { marginHorizontal: 16, marginBottom: 8, minHeight: 42, borderRadius: 12, backgroundColor: theme.stop, alignItems: "center", justifyContent: "center", overflow: "hidden" },
    stopText: { color: "#fff", fontWeight: "800" }
  });
}

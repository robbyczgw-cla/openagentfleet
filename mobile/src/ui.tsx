import { useMemo } from "react";
import { ActivityIndicator, Platform, Pressable, StyleSheet, Text, View } from "react-native";

import type { ConnectionState } from "./api/types";
import { connectionLabel } from "./session";
import { type ThemeColors, useTheme } from "./theme";

export type Screen = "chat" | "computer" | "routines" | "settings";

export function StatusBanner(props: { state: ConnectionState }) {
  const theme = useTheme();
  const styles = useMemo(() => createBannerStyles(theme), [theme]);
  if (props.state === "connected") return null;
  const label = props.state === "degraded" ? "Reconnecting…" : props.state === "connecting" ? "Connecting…" : "Disconnected from your Mac";
  return (
    <View style={styles.banner} accessibilityRole="alert">
      {props.state !== "disconnected" ? <ActivityIndicator size="small" color={theme.bannerText} /> : null}
      <Text style={styles.bannerText}>{label}</Text>
    </View>
  );
}

export function StatusPill(props: { state: ConnectionState }) {
  const theme = useTheme();
  const styles = useMemo(() => createChromeStyles(theme), [theme]);
  const tone = props.state === "connected" ? styles.statusGood : props.state === "degraded" ? styles.statusWarn : styles.statusNeutral;
  return (
    <View style={[styles.status, tone]}>
      <View style={styles.statusDot} />
      <Text style={styles.statusText}>{connectionLabel(props.state)}</Text>
    </View>
  );
}

export function TabBar(props: { screen: Screen; onChange: (screen: Screen) => void }) {
  const theme = useTheme();
  const styles = useMemo(() => createChromeStyles(theme), [theme]);
  return (
    <View style={styles.tabs}>
      <Tab label="Chat" active={props.screen === "chat"} onPress={() => props.onChange("chat")} />
      <Tab label="Computer" active={props.screen === "computer"} onPress={() => props.onChange("computer")} />
      <Tab label="Routines" active={props.screen === "routines"} onPress={() => props.onChange("routines")} />
      <Tab label="Settings" active={props.screen === "settings"} onPress={() => props.onChange("settings")} />
    </View>
  );
}

function Tab(props: { label: string; active: boolean; onPress: () => void }) {
  const theme = useTheme();
  const styles = useMemo(() => createChromeStyles(theme), [theme]);
  return (
    <Pressable
      accessibilityRole="tab"
      accessibilityState={{ selected: props.active }}
      android_ripple={{ color: theme.ripple }}
      style={styles.tab}
      onPress={props.onPress}
    >
      <Text style={props.active ? styles.tabActive : styles.tabText}>{props.label}</Text>
      {props.active ? <View style={styles.tabIndicator} /> : <View style={styles.tabIndicatorOff} />}
    </Pressable>
  );
}

function createBannerStyles(theme: ThemeColors) {
  return StyleSheet.create({
    banner: {
      flexDirection: "row",
      alignItems: "center",
      gap: 8,
      paddingHorizontal: 16,
      paddingVertical: 8,
      backgroundColor: theme.bannerBg
    },
    bannerText: { color: theme.bannerText, fontWeight: "700", fontSize: 13 }
  });
}

function createChromeStyles(theme: ThemeColors) {
  return StyleSheet.create({
    status: { flexDirection: "row", alignItems: "center", gap: 6, borderRadius: 999, paddingHorizontal: 10, paddingVertical: 6 },
    statusGood: { backgroundColor: theme.statusGoodBg },
    statusWarn: { backgroundColor: theme.statusWarnBg },
    statusNeutral: { backgroundColor: theme.statusNeutralBg },
    statusDot: { width: 7, height: 7, backgroundColor: theme.statusDot, borderRadius: 4 },
    statusText: { color: theme.statusText, fontWeight: "700", fontSize: 12 },
    tabs: {
      flexDirection: "row",
      borderTopWidth: StyleSheet.hairlineWidth,
      borderColor: theme.border,
      backgroundColor: theme.background,
      paddingTop: 4,
      paddingBottom: Platform.OS === "android" ? 10 : 6
    },
    tab: { flex: 1, minHeight: 44, justifyContent: "center", alignItems: "center", overflow: "hidden" },
    tabText: { color: theme.tabInactive, fontSize: 12, fontWeight: "700" },
    tabActive: { color: theme.text, fontSize: 12, fontWeight: "800" },
    tabIndicator: { marginTop: 6, width: 18, height: 3, borderRadius: 2, backgroundColor: theme.text },
    tabIndicatorOff: { marginTop: 6, width: 18, height: 3 }
  });
}

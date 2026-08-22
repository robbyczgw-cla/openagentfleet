import { useMemo } from "react";
import { Pressable, ScrollView, StyleSheet, Text, View } from "react-native";

import type { ComputerStatus, ConnectionState, RemoteProfile } from "../api/types";
import { connectionLabel } from "../session";
import { type ThemeColors, useTheme } from "../theme";

export function SettingsScreen(props: {
  profile: RemoteProfile;
  state: ConnectionState;
  computer?: ComputerStatus;
  onDisconnect: () => void;
}) {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  return (
    <ScrollView contentContainerStyle={styles.settings}>
      <Text style={styles.sectionTitle}>Connection</Text>
      <Setting label="Host" value={props.profile.baseUrl} />
      <Setting label="Host identity" value={props.profile.hostId} />
      <Setting label="Device" value={props.profile.device.name} />
      <Setting label="Connection" value={connectionLabel(props.state)} />
      <Setting label="Agent Computer" value={props.computer?.running ? "Running" : "Not running"} />
      <Pressable
        accessibilityRole="button"
        android_ripple={{ color: theme.ripple }}
        style={styles.destructiveButton}
        onPress={props.onDisconnect}
      >
        <Text style={styles.destructiveText}>Disconnect</Text>
      </Pressable>
    </ScrollView>
  );
}

function Setting(props: { label: string; value: string }) {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  return (
    <View style={styles.setting}>
      <Text style={styles.settingLabel}>{props.label}</Text>
      <Text selectable style={styles.settingValue}>
        {props.value}
      </Text>
    </View>
  );
}

function createStyles(theme: ThemeColors) {
  return StyleSheet.create({
    settings: { padding: 20, gap: 8 },
    sectionTitle: { fontSize: 20, color: theme.text, fontWeight: "700" },
    setting: { paddingVertical: 14, borderBottomWidth: StyleSheet.hairlineWidth, borderColor: theme.border },
    settingLabel: { color: theme.textMuted, fontSize: 12, fontWeight: "700" },
    settingValue: { color: theme.text, fontSize: 16, marginTop: 4 },
    destructiveButton: {
      borderWidth: 1,
      borderColor: theme.dangerBorder,
      borderRadius: 14,
      minHeight: 50,
      alignItems: "center",
      justifyContent: "center",
      marginTop: 16,
      overflow: "hidden"
    },
    destructiveText: { color: theme.dangerText, fontWeight: "800" }
  });
}

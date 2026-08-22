import { useMemo } from "react";
import { Pressable, ScrollView, StyleSheet, Text, View } from "react-native";

import type { MobileRoutine } from "../api/types";
import { type ThemeColors, useTheme } from "../theme";

export function RoutinesScreen(props: {
  routines: MobileRoutine[];
  error?: string;
  canControl: boolean;
  busy: boolean;
  onPause: (routine: MobileRoutine) => void;
  onEnable: (routine: MobileRoutine) => void;
  onRefresh: () => void;
}) {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  return (
    <ScrollView contentContainerStyle={styles.settings} keyboardShouldPersistTaps="handled">
      <View style={styles.computerTop}>
        <View style={{ flex: 1 }}>
          <Text style={styles.sectionTitle}>Scheduled work</Text>
          <Text style={styles.subtle}>{props.canControl ? "Pause or enable from this phone." : "Observer devices can list routines only."}</Text>
        </View>
        <Pressable accessibilityRole="button" android_ripple={{ color: theme.ripple }} style={styles.secondaryButton} onPress={props.onRefresh}>
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
            <Pressable
              accessibilityRole="button"
              disabled={props.busy}
              android_ripple={{ color: theme.ripple }}
              style={[styles.secondaryButton, styles.routineAction, props.busy && styles.disabled]}
              onPress={() => props.onPause(routine)}
            >
              <Text style={styles.secondaryText}>Pause</Text>
            </Pressable>
          ) : null}
          {props.canControl && (routine.status === "paused" || routine.status === "disabled") ? (
            <Pressable
              accessibilityRole="button"
              disabled={props.busy}
              android_ripple={{ color: theme.ripple }}
              style={[styles.secondaryButton, styles.routineAction, props.busy && styles.disabled]}
              onPress={() => props.onEnable(routine)}
            >
              <Text style={styles.secondaryText}>Enable</Text>
            </Pressable>
          ) : null}
        </View>
      ))}
    </ScrollView>
  );
}

function createStyles(theme: ThemeColors) {
  return StyleSheet.create({
    settings: { padding: 20, gap: 8 },
    computerTop: { flexDirection: "row", alignItems: "center", justifyContent: "space-between", gap: 12 },
    sectionTitle: { fontSize: 20, color: theme.text, fontWeight: "700" },
    subtle: { color: theme.textSubtle, marginTop: 4, lineHeight: 19 },
    secondaryButton: { paddingHorizontal: 13, paddingVertical: 9, borderRadius: 10, backgroundColor: theme.secondaryBg, overflow: "hidden" },
    secondaryText: { color: theme.secondaryText, fontWeight: "700" },
    error: { color: theme.error, marginTop: 12, lineHeight: 20 },
    routineRow: { paddingVertical: 14, borderBottomWidth: StyleSheet.hairlineWidth, borderColor: theme.border },
    routineName: { color: theme.text, fontSize: 17, fontWeight: "700" },
    routineMeta: { color: theme.textSubtle, marginTop: 4, lineHeight: 19 },
    routineAction: { alignSelf: "flex-start", marginTop: 10 },
    disabled: { opacity: 0.45 }
  });
}

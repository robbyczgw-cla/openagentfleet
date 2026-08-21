import { useMemo, useState } from "react";
import {
  ActivityIndicator,
  type GestureResponderEvent,
  Image,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  useWindowDimensions,
  View
} from "react-native";

import type { ComputerStatus } from "../api/types";
import { type ThemeColors, useTheme } from "../theme";

export function ComputerScreen(props: {
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
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const { height: windowHeight } = useWindowDimensions();
  const frameHeight = Math.round(Math.min(windowHeight * 0.58, Math.max(420, windowHeight - 280)));
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
        <Pressable
          accessibilityRole="button"
          disabled={props.loading}
          android_ripple={{ color: theme.ripple }}
          style={[styles.secondaryButton, props.loading && styles.disabled]}
          onPress={props.onRefresh}
        >
          {props.loading ? <ActivityIndicator color={theme.secondaryText} /> : <Text style={styles.secondaryText}>Refresh</Text>}
        </Pressable>
      </View>
      <Pressable
        accessibilityRole="imagebutton"
        accessibilityLabel={props.controlHeld ? "Click the remote desktop" : "Read-only Agent Computer frame"}
        disabled={!props.controlHeld || !ready || props.controlBusy}
        style={[styles.frame, { height: frameHeight }]}
        onLayout={(event) => setFrameBounds({ width: event.nativeEvent.layout.width, height: event.nativeEvent.layout.height })}
        onPress={pressFrame}
      >
        {ready ? (
          <Image source={{ uri: props.frame }} style={styles.frameImage} resizeMode="contain" />
        ) : (
          <View style={styles.emptyFrame}>
            <Text style={styles.emptyTitle}>{props.frameError ? "Frame unavailable" : "Computer not ready"}</Text>
            <Text style={styles.emptyCopy}>{props.frameError || "Start the computer from the desktop app."}</Text>
          </View>
        )}
      </Pressable>
      <Text style={styles.frameCaption}>
        {props.controlHeld
          ? "Short click lease · passwords stay on the desktop"
          : "Watch-only until you take a short click lease"}
      </Text>
      <Pressable
        accessibilityRole="button"
        disabled={props.controlBusy}
        android_ripple={{ color: theme.rippleOnPrimary }}
        style={[styles.primaryButton, { marginTop: 12 }, props.controlBusy && styles.disabled]}
        onPress={props.onToggleControl}
      >
        <Text style={styles.primaryText}>{props.controlBusy ? "Working…" : props.controlHeld ? "Release control" : "Take control"}</Text>
      </Pressable>
    </ScrollView>
  );
}

function createStyles(theme: ThemeColors) {
  return StyleSheet.create({
    computerContent: { padding: 16 },
    computerTop: { flexDirection: "row", alignItems: "center", justifyContent: "space-between", gap: 12 },
    sectionTitle: { fontSize: 20, color: theme.text, fontWeight: "700" },
    subtle: { color: theme.textSubtle, marginTop: 4, lineHeight: 19 },
    emptyCopy: { color: theme.frameCaption, marginTop: 4, lineHeight: 19, textAlign: "center" },
    secondaryButton: { paddingHorizontal: 13, paddingVertical: 9, borderRadius: 10, backgroundColor: theme.secondaryBg, overflow: "hidden" },
    secondaryText: { color: theme.secondaryText, fontWeight: "700" },
    frame: { marginTop: 16, borderRadius: 16, overflow: "hidden", backgroundColor: theme.frameBg },
    frameImage: { width: "100%", height: "100%" },
    emptyFrame: { flex: 1, justifyContent: "center", alignItems: "center", padding: 20 },
    emptyTitle: { color: theme.emptyTitle, fontWeight: "700", fontSize: 18 },
    frameCaption: { color: theme.frameCaption, fontSize: 12, marginTop: 8 },
    primaryButton: { minHeight: 52, borderRadius: 14, backgroundColor: theme.primary, alignItems: "center", justifyContent: "center", overflow: "hidden" },
    primaryText: { color: theme.primaryText, fontSize: 16, fontWeight: "700" },
    disabled: { opacity: 0.45 }
  });
}

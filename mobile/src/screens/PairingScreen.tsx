import { useMemo, useRef, useState } from "react";
import {
  ActivityIndicator,
  Alert,
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

import { type ThemeColors, useStatusBarStyle, useTheme } from "../theme";

export function PairingScreen(props: {
  pairingBundle: string;
  setPairingBundle: (value: string) => void;
  connecting: boolean;
  scanning: boolean;
  setScanning: (value: boolean) => void;
  error: string | null;
  onPair: () => void;
  onScanned: (value: string) => void;
}) {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const barStyle = useStatusBarStyle();
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
        <Pressable
          accessibilityRole="button"
          android_ripple={{ color: "#00000022" }}
          style={styles.scannerClose}
          onPress={() => props.setScanning(false)}
        >
          <Text style={styles.scannerCloseText}>Cancel</Text>
        </Pressable>
      </View>
    );
  }

  return (
    <SafeAreaView style={styles.safe}>
      <StatusBar barStyle={barStyle} backgroundColor={theme.background} />
      <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === "ios" ? "padding" : undefined}>
        <ScrollView contentContainerStyle={styles.setup} keyboardShouldPersistTaps="handled">
          <Text style={styles.eyebrow}>OpenAgentFleet</Text>
          <Text style={styles.hero}>Connect this phone to your computer.</Text>
          <Text style={styles.lede}>
            Scan the pairing QR from the desktop app. This phone is a remote on your Tailnet, not a second runtime.
          </Text>
          {props.error ? <Text style={styles.error}>{props.error}</Text> : null}
          <Pressable
            accessibilityRole="button"
            disabled={props.connecting}
            android_ripple={{ color: theme.rippleOnPrimary }}
            style={[styles.primaryButton, props.connecting && styles.disabled]}
            onPress={() => void startScan()}
          >
            {props.connecting ? <ActivityIndicator color={theme.primaryText} /> : <Text style={styles.primaryText}>Scan pairing QR</Text>}
          </Pressable>
          <Pressable accessibilityRole="button" android_ripple={{ color: theme.ripple }} onPress={() => setPasteOpen((value) => !value)}>
            <Text style={styles.textLink}>{pasteOpen ? "Hide paste field" : "Paste JSON instead"}</Text>
          </Pressable>
          {pasteOpen ? (
            <>
              <TextInput
                autoCapitalize="none"
                autoCorrect={false}
                multiline
                placeholder='{"version":1,...}'
                placeholderTextColor={theme.placeholder}
                style={[styles.input, styles.bundleInput]}
                value={props.pairingBundle}
                onChangeText={props.setPairingBundle}
              />
              <Pressable
                accessibilityRole="button"
                disabled={props.connecting || !props.pairingBundle.trim()}
                android_ripple={{ color: theme.ripple }}
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

function createStyles(theme: ThemeColors) {
  return StyleSheet.create({
    safe: { flex: 1, backgroundColor: theme.background, paddingTop: Platform.OS === "android" ? StatusBar.currentHeight || 0 : 0 },
    eyebrow: { color: theme.textMuted, fontSize: 12, fontWeight: "700", letterSpacing: 0.2 },
    hero: { color: theme.text, fontSize: 34, lineHeight: 40, fontWeight: "700", letterSpacing: -0.8, marginTop: 12 },
    lede: { color: theme.textLede, fontSize: 16, lineHeight: 23, marginTop: 14 },
    setup: { padding: 24, flexGrow: 1, justifyContent: "center" },
    input: {
      backgroundColor: theme.surface,
      color: theme.text,
      borderColor: theme.inputBorder,
      borderWidth: 1,
      borderRadius: 14,
      minHeight: 54,
      paddingHorizontal: 15,
      fontSize: 15,
      marginTop: 16
    },
    bundleInput: { minHeight: 128, paddingVertical: 14, textAlignVertical: "top" },
    primaryButton: { minHeight: 52, borderRadius: 14, backgroundColor: theme.primary, alignItems: "center", justifyContent: "center", marginTop: 22, overflow: "hidden" },
    primaryText: { color: theme.primaryText, fontSize: 16, fontWeight: "700" },
    secondaryButton: { paddingHorizontal: 13, paddingVertical: 9, borderRadius: 10, backgroundColor: theme.secondaryBg, overflow: "hidden" },
    secondaryText: { color: theme.secondaryText, fontWeight: "700" },
    disabled: { opacity: 0.45 },
    error: { color: theme.error, marginTop: 12, lineHeight: 20 },
    textLink: { color: theme.text, fontWeight: "700", fontSize: 15, marginTop: 18, textDecorationLine: "underline" },
    pairPaste: { marginTop: 12, alignSelf: "flex-start" },
    scanner: { flex: 1, backgroundColor: "#11110e" },
    scannerMask: { position: "absolute", top: 0, right: 0, bottom: 0, left: 0, alignItems: "center", justifyContent: "center" },
    scannerFrame: { width: 240, height: 240, borderRadius: 24, borderWidth: 3, borderColor: "#f6f3ec" },
    scannerHint: { color: "#f6f3ec", marginTop: 18, fontSize: 15, fontWeight: "600" },
    scannerClose: { position: "absolute", bottom: 36, alignSelf: "center", paddingHorizontal: 22, paddingVertical: 12, borderRadius: 999, backgroundColor: "#f6f3ec", overflow: "hidden" },
    scannerCloseText: { color: "#1b1813", fontWeight: "800" }
  });
}

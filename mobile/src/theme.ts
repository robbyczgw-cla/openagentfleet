import { useColorScheme } from "react-native";

export interface ThemeColors {
  background: string;
  surface: string;
  surfaceMuted: string;
  text: string;
  textMuted: string;
  textSubtle: string;
  textLede: string;
  border: string;
  input: string;
  inputBorder: string;
  placeholder: string;
  primary: string;
  primaryText: string;
  error: string;
  dangerBorder: string;
  dangerText: string;
  dangerFill: string;
  stop: string;
  approvalBg: string;
  approvalTitle: string;
  otherApprovals: string;
  statusGoodBg: string;
  statusWarnBg: string;
  statusNeutralBg: string;
  statusText: string;
  statusDot: string;
  frameBg: string;
  frameCaption: string;
  emptyTitle: string;
  tabInactive: string;
  secondaryBg: string;
  secondaryText: string;
  bannerBg: string;
  bannerText: string;
  ripple: string;
  rippleOnPrimary: string;
  composerBg: string;
}

/** Paper palette used by the existing companion in light mode. */
export const light: ThemeColors = {
  background: "#f6f3ec",
  surface: "#fffefa",
  surfaceMuted: "#ece9e2",
  text: "#1b1813",
  textMuted: "#6f6a62",
  textSubtle: "#716e66",
  textLede: "#58544c",
  border: "#e2ddd4",
  input: "#ffffff",
  inputBorder: "#d8d2c7",
  placeholder: "#8e8a82",
  primary: "#1b1813",
  primaryText: "#f6f3ec",
  error: "#a42920",
  dangerBorder: "#bb4a42",
  dangerText: "#a8322a",
  dangerFill: "#f3d7d3",
  stop: "#bb4a42",
  approvalBg: "#efe6d2",
  approvalTitle: "#5a3b12",
  otherApprovals: "#7a5a20",
  statusGoodBg: "#dcebd9",
  statusWarnBg: "#f2e3ba",
  statusNeutralBg: "#e7e4de",
  statusText: "#3d4038",
  statusDot: "#35654d",
  frameBg: "#1b1813",
  frameCaption: "#827e74",
  emptyTitle: "#ffffff",
  tabInactive: "#817d74",
  secondaryBg: "#e8e5dc",
  secondaryText: "#3e4039",
  bannerBg: "#efe3c4",
  bannerText: "#5a3b12",
  ripple: "#00000014",
  rippleOnPrimary: "#ffffff33",
  composerBg: "#f6f3ec"
};

/** Warm dark palette; not a straight invert of the paper colors. */
export const dark: ThemeColors = {
  background: "#12110f",
  surface: "#1c1b18",
  surfaceMuted: "#26241f",
  text: "#f3efe6",
  textMuted: "#a39e94",
  textSubtle: "#9a958b",
  textLede: "#b7b2a8",
  border: "#2e2c28",
  input: "#1a1916",
  inputBorder: "#3a3732",
  placeholder: "#7d786f",
  primary: "#f3efe6",
  primaryText: "#161512",
  error: "#e07068",
  dangerBorder: "#c45c54",
  dangerText: "#f0a29c",
  dangerFill: "#3a1f1c",
  stop: "#b04a42",
  approvalBg: "#3a3220",
  approvalTitle: "#e6c98a",
  otherApprovals: "#d4b56a",
  statusGoodBg: "#1c3328",
  statusWarnBg: "#3a3220",
  statusNeutralBg: "#2a2824",
  statusText: "#d8d4cc",
  statusDot: "#7dba90",
  frameBg: "#0c0b0a",
  frameCaption: "#8a857c",
  emptyTitle: "#f3efe6",
  tabInactive: "#8f8a81",
  secondaryBg: "#2a2824",
  secondaryText: "#e6e1d8",
  bannerBg: "#3a3220",
  bannerText: "#e6c98a",
  ripple: "#ffffff22",
  rippleOnPrimary: "#00000022",
  composerBg: "#12110f"
};

export type Theme = ThemeColors;

export function useTheme(): ThemeColors {
  return useColorScheme() === "dark" ? dark : light;
}

export function useStatusBarStyle(): "light-content" | "dark-content" {
  return useColorScheme() === "dark" ? "light-content" : "dark-content";
}

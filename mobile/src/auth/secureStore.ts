import * as SecureStore from "expo-secure-store";

import type { RemoteProfile } from "../api/types";

const TOKEN_KEY = "openagentfleet.remote.v1.access-token";
const PROFILE_KEY = "openagentfleet.remote.v1.profile";
const SECURE_OPTIONS = { keychainAccessible: SecureStore.WHEN_UNLOCKED_THIS_DEVICE_ONLY } as const;

export interface SecureTokenStore {
  readToken(): Promise<string | null>;
  saveToken(token: string): Promise<void>;
  deleteToken(): Promise<void>;
}

export interface ConnectionProfileStore {
  readProfile(): Promise<RemoteProfile | null>;
  saveProfile(profile: RemoteProfile): Promise<void>;
  clear(): Promise<void>;
}

export const secureTokenStore: SecureTokenStore = {
  readToken: () => SecureStore.getItemAsync(TOKEN_KEY),
  saveToken: (token) => SecureStore.setItemAsync(TOKEN_KEY, token, SECURE_OPTIONS),
  deleteToken: () => SecureStore.deleteItemAsync(TOKEN_KEY)
};

function isProfile(value: unknown): value is RemoteProfile {
  if (!value || typeof value !== "object") return false;
  const profile = value as Partial<RemoteProfile>;
  return typeof profile.baseUrl === "string" && typeof profile.hostId === "string" && profile.authVersion === 1
    && Boolean(profile.device) && typeof profile.device?.name === "string" && typeof profile.device?.platform === "string";
}

export const connectionProfileStore: ConnectionProfileStore = {
  async readProfile() {
    const value = await SecureStore.getItemAsync(PROFILE_KEY);
    if (!value) return null;
    try {
      const parsed: unknown = JSON.parse(value);
      return isProfile(parsed) ? parsed : null;
    } catch {
      return null;
    }
  },
  saveProfile: (profile) => SecureStore.setItemAsync(PROFILE_KEY, JSON.stringify(profile), SECURE_OPTIONS),
  async clear() {
    await Promise.all([SecureStore.deleteItemAsync(PROFILE_KEY), SecureStore.deleteItemAsync(TOKEN_KEY)]);
  }
};

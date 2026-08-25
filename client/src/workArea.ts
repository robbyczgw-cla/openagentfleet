export const WINDOWS_TASKBAR_RESERVE_PX = 48;

export type WorkAreaInput = {
  innerHeight: number;
  screenHeight: number;
  availHeight: number;
  safeAreaBottom?: number;
};

/** Bottom inset so sticky dialogs stay above a Windows taskbar or safe area. */
export function workAreaBottomInsetPx(input: WorkAreaInput): number {
  const safe = Math.max(0, input.safeAreaBottom ?? 0);
  const screenHeight = Math.max(0, input.screenHeight);
  const availHeight = Math.max(0, input.availHeight);
  const innerHeight = Math.max(0, input.innerHeight);
  let taskbar = 0;
  const windowCoversScreen =
    screenHeight > 0 && innerHeight + 8 >= screenHeight;
  const workAreaShorter =
    screenHeight > 0 && availHeight > 0 && availHeight < screenHeight;
  if (windowCoversScreen || (workAreaShorter && innerHeight + 8 >= availHeight)) {
    const reported = workAreaShorter ? screenHeight - availHeight : WINDOWS_TASKBAR_RESERVE_PX;
    taskbar = Math.min(96, Math.max(WINDOWS_TASKBAR_RESERVE_PX, reported));
  }
  return Math.max(12, safe, taskbar);
}

export function readSafeAreaBottomPx(style: { getPropertyValue(name: string): string }): number {
  for (const name of ["padding-bottom", "--safe-area-inset-bottom"] as const) {
    const raw = style.getPropertyValue(name);
    const parsed = Number.parseFloat(raw);
    if (Number.isFinite(parsed)) return parsed;
  }
  return 0;
}

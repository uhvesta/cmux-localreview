let debugEnabled = !!process.env.CMUX_HUB_DEBUG;

export function enableDebug() {
  debugEnabled = true;
}

export const logger = {
  debug: (...args: unknown[]) => {
    if (debugEnabled) console.log("[cmux-hub:debug]", ...args);
  },
  info: (...args: unknown[]) => {
    console.log("[cmux-hub]", ...args);
  },
};

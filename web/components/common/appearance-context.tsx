'use client';

import { createContext, useContext, useState, useEffect, useRef, type ReactNode } from 'react';

const ACCENTS = ['indigo', 'teal', 'mono'] as const;
type Accent = (typeof ACCENTS)[number];
const TONES = ['vivid', 'muted'] as const;
type AgentTone = (typeof TONES)[number];

interface AppearanceValues {
  accent: Accent;
  agentTone: AgentTone;
  showLogoMark: boolean;
}

interface AppearanceState extends AppearanceValues {
  setAccent: (v: Accent) => void;
  setAgentTone: (v: AgentTone) => void;
  setShowLogoMark: (v: boolean) => void;
}

interface AppearancePayload {
  accent?: unknown;
  agentTone?: unknown;
  showLogoMark?: unknown;
}

const DEFAULTS: AppearanceValues = {
  // Neutral, because a selection colour is chrome and the data palette has no hue
  // left to spare — see the note on --accent in globals.css. This must agree with
  // that base: applyToDom deletes the attribute when the value equals the default,
  // so whatever is named here is what :root renders with no [data-accent] rule.
  accent: 'mono',
  agentTone: 'vivid',
  showLogoMark: true,
};

const STORAGE_KEY = 'cctrace.appearance';

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

const isAccent = (value: unknown): value is Accent =>
  typeof value === 'string' && ACCENTS.includes(value as Accent);

const isAgentTone = (value: unknown): value is AgentTone =>
  typeof value === 'string' && TONES.includes(value as AgentTone);

const normalizeAppearancePayload = (payload: unknown): AppearanceValues => {
  const candidate = (isRecord(payload) ? payload : {}) as AppearancePayload;
  return {
    accent: isAccent(candidate.accent) ? candidate.accent : DEFAULTS.accent,
    agentTone: isAgentTone(candidate.agentTone) ? candidate.agentTone : DEFAULTS.agentTone,
    showLogoMark: typeof candidate.showLogoMark === 'boolean'
      ? candidate.showLogoMark
      : DEFAULTS.showLogoMark,
  };
};

const parseAppearancePayload = (raw: string | null): AppearanceValues => {
  if (!raw) return { ...DEFAULTS };
  try {
    return normalizeAppearancePayload(JSON.parse(raw));
  } catch {
    return { ...DEFAULTS };
  }
};

const serializeAppearancePayload = (values: AppearanceValues): string =>
  JSON.stringify({
    accent: values.accent,
    agentTone: values.agentTone,
    showLogoMark: values.showLogoMark,
  });

const AppearanceContext = createContext<AppearanceState>({
  ...DEFAULTS,
  setAccent: () => {},
  setAgentTone: () => {},
  setShowLogoMark: () => {},
});

/** Reflect the supported tweak state onto :root data-attributes. */
const applyToDom = (accent: Accent, agentTone: AgentTone): void => {
  const root = document.documentElement;
  delete root.dataset.density;
  if (accent === DEFAULTS.accent) delete root.dataset.accent;
  else root.dataset.accent = accent;
  if (agentTone === DEFAULTS.agentTone) delete root.dataset.agenttone;
  else root.dataset.agenttone = agentTone;
};

const AppearanceProvider = ({ children }: { children: ReactNode }) => {
  const [accent, setAccent] = useState<Accent>(DEFAULTS.accent);
  const [agentTone, setAgentTone] = useState<AgentTone>(DEFAULTS.agentTone);
  const [showLogoMark, setShowLogoMark] = useState<boolean>(DEFAULTS.showLogoMark);

  const hydrated = useRef(false);

  /** 마운트 후 localStorage에서 1회 복원. 손상/위조 값은 검증해 무시(잘못된 data-* 방지).
   * 정적 export 하이드레이션 불일치를 피하려 effect에서 setState. */
  useEffect(() => {
    try {
      const restored = parseAppearancePayload(localStorage.getItem(STORAGE_KEY));
      /* eslint-disable react-hooks/set-state-in-effect -- 의도된 post-mount 하이드레이션 */
      setAccent(restored.accent);
      setAgentTone(restored.agentTone);
      setShowLogoMark(restored.showLogoMark);
      /* eslint-enable react-hooks/set-state-in-effect */
    } catch { /* ignore */ }
    hydrated.current = true;
  }, []);

  /** 시각 tweak만 :root에 반영(로고는 CSS 변수가 아니라 제외). */
  useEffect(() => {
    applyToDom(accent, agentTone);
  }, [accent, agentTone]);

  /** 변경 시 영속화(복원 완료 전 기본값 덮어쓰기 방지). */
  useEffect(() => {
    if (!hydrated.current) return;
    try {
      localStorage.setItem(STORAGE_KEY, serializeAppearancePayload({ accent, agentTone, showLogoMark }));
    } catch { /* ignore */ }
  }, [accent, agentTone, showLogoMark]);

  return (
    <AppearanceContext.Provider
      value={{ accent, agentTone, showLogoMark, setAccent, setAgentTone, setShowLogoMark }}
    >
      {children}
    </AppearanceContext.Provider>
  );
};

const useAppearance = () => useContext(AppearanceContext);

export {
  AppearanceProvider,
  useAppearance,
  applyToDom,
  normalizeAppearancePayload,
  parseAppearancePayload,
  serializeAppearancePayload,
};
export type { Accent, AgentTone, AppearanceValues };

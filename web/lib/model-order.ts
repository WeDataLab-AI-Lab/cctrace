import { agentOf } from '@/lib/colors';

/**
 * 범례에 쓰는 모델 표시 순서 — 계열 → 티어 → 버전(내림차순).
 *
 * 스택 순서(activeKeys)는 기간마다 금액에 따라 바뀌지만, 범례는 어떤 기간을
 * 보든 같은 자리에 같은 모델이 오도록 고정한다. 색-모델 대응을 기간마다 다시
 * 읽지 않아도 되게 하는 것이 목적이다.
 *
 * 입력은 modelLabel()이 만든 표시 라벨('opus 4.8', 'gpt-5.6-sol', 'gemma4:12b').
 */

// agentOf(colors.ts SSOT)가 돌려주는 계열을 표시 순서로 옮긴다.
const AGENT_RANK: Record<string, number> = { claude: 0, codex: 1, gjc: 2, omo: 3, compat: 4 };

// 계열별 티어 사다리. 없는 계열은 전부 동률이라 버전·사전순으로 내려간다.
//
// codex 사다리는 codex_model_rates 의 단가 순서와 같다 — sol 5.00 > terra 2.50 >
// luna 1.00. 셋 다 gpt-5.6 이라 버전으로는 갈리지 않고, 사전순은 luna 를 sol 앞에
// 세워 비싼 모델이 가운데 오는 순서가 됐다.
//
// gpt-6-astra 는 여기 없다 — codex 는 버전이 티어보다 먼저라(TIER_BEFORE_VERSION 참조),
// 세대 번호(6 > 5.x)만으로 이미 sol을 포함한 모든 gpt-5.x 앞에 선다. 같은 세대(gpt-6.x)
// 안에 티어가 갈리는 두 번째 모델이 나오기 전까지는 넣을 필요가 없다.
const TIERS: Record<string, string[]> = {
  claude: ['fable', 'opus', 'sonnet', 'haiku'],
  codex: ['sol', 'terra', 'luna'],
};

// 티어 이름이 없는 라벨(gpt-5.4 같은 세대 기본 모델)은 named 티어 뒤, mini 앞이다.
// mini 를 사다리에 넣으면 gpt-5.4-mini 가 gpt-5.4 보다 앞서게 된다.
const isMini = (label: string): boolean => label.toLowerCase().includes('mini');

// 티어가 버전보다 먼저인 계열. Claude 의 fable·opus·sonnet·haiku 는 세대를 가로지르는
// 제품군이라 티어로 먼저 묶이지만, codex 의 sol·terra·luna 는 한 세대(gpt-5.6) 안의
// 변종이라 세대가 먼저다 — 티어를 먼저 보면 gpt-5.6-luna 가 gpt-5.5 뒤로 밀린다.
const TIER_BEFORE_VERSION = new Set(['claude']);

const isOthers = (label: string): boolean => {
  const l = label.toLowerCase();
  return l === 'others' || l === 'other';
};

const tierRank = (label: string, agent: string): number => {
  const tiers = TIERS[agent];
  if (!tiers) return 0;
  if (isMini(label)) return tiers.length + 1;
  const l = label.toLowerCase();
  const i = tiers.findIndex((t) => l.includes(t));
  return i === -1 ? tiers.length : i;
};

/** 첫 번째 버전 토큰을 세그먼트 배열로. 없으면 null(= 버전 있는 라벨보다 뒤). */
const versionSegments = (label: string): number[] | null => {
  const m = label.match(/(?:^|[\s-])(\d+(?:\.\d+)*)(?:[\s-]|$)/);
  return m ? m[1].split('.').map(Number) : null;
};

/** 세그먼트 단위 숫자 비교 — '4.10'을 '4.9'보다 위로 올린다. */
const compareVersionsDesc = (a: number[] | null, b: number[] | null): number => {
  if (!a && !b) return 0;
  if (!a) return 1;
  if (!b) return -1;
  for (let i = 0; i < Math.max(a.length, b.length); i++) {
    const d = (b[i] ?? 0) - (a[i] ?? 0);
    if (d !== 0) return d;
  }
  return 0;
};

/** Array.prototype.sort 비교자 — 표시 라벨을 정규 순서로 세운다. */
export function compareModelLabels(a: string, b: string): number {
  const othersA = isOthers(a);
  const othersB = isOthers(b);
  if (othersA !== othersB) return othersA ? 1 : -1;

  const agentA = agentOf(a);
  const agentB = agentOf(b);
  const agentDiff = (AGENT_RANK[agentA] ?? AGENT_RANK.compat) - (AGENT_RANK[agentB] ?? AGENT_RANK.compat);
  if (agentDiff !== 0) return agentDiff;

  const tierDiff = tierRank(a, agentA) - tierRank(b, agentB);
  const versionDiff = compareVersionsDesc(versionSegments(a), versionSegments(b));
  if (TIER_BEFORE_VERSION.has(agentA)) {
    if (tierDiff !== 0) return tierDiff;
    if (versionDiff !== 0) return versionDiff;
  } else {
    if (versionDiff !== 0) return versionDiff;
    if (tierDiff !== 0) return tierDiff;
  }

  return a.localeCompare(b);
}

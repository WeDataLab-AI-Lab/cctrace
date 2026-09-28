import { agentOf } from '@/lib/colors';
import { modelLabel, shortModel } from '@/lib/model-label';
import type { ModelDatum } from './overview-helpers';

interface CostUserStackRow {
  user_id?: string;
  model: string;
  agent?: string;
  billing_provider?: string;
  total_cost: number;
  total_input_tokens: number;
  total_output_tokens: number;
}

interface CostUserStackDatum {
  name: string;
  [stackKey: string]: number | string;
}

interface CostModelRow {
  model: string;
  agent?: string;
  billing_provider?: string;
  total_cost: number;
  input_tokens: number;
  output_tokens: number;
}

type CostModelDatum = ModelDatum;

interface BuildCostUserStackDataParams {
  rows: CostUserStackRow[];
  nameMap: Record<string, string>;
  viewMode: 'cost' | 'token';
}

interface BuildCostModelDataParams {
  rows: CostModelRow[];
  viewMode: 'cost' | 'token';
}

interface CostUserStackResult {
  data: CostUserStackDatum[];
  stackKeys: CostUserStackKey[];
}

interface CostModelResult {
  data: CostModelDatum[];
  otherNames: string[];
}

type CostUserStackKey = string;

const OTHERS_STACK_KEY = 'Others';

// 분류는 agentOf(colors.ts SSOT)에 위임: claude|codex|compat 3종.
// compat(qwen/glm/kimi 등)는 Others로 접지 않고 자기 키를 갖는다.
// 라벨 자체는 web/lib/model-label.ts(SSOT)에 위임 — 필터 드롭다운·트렌드 차트와 동일 표시값을 보장한다.
const costUserStackKey = (model: string): CostUserStackKey => modelLabel(model);

const stackFamilyRank = (key: string): number => {
  const family = agentOf(key);
  if (family === 'claude') return 0;
  if (family === 'codex') return 1;
  return 2;
};

const claudeTierRank = (key: string): number => {
  const model = key.toLowerCase();
  if (model.includes('fable')) return 0;
  if (model.includes('opus')) return 1;
  if (model.includes('sonnet')) return 2;
  if (model.includes('haiku')) return 3;
  return 4;
};

const codexTierRank = (key: string): number => {
  const model = key.toLowerCase();
  if (model.includes('gpt-5.6-sol')) return 0;
  if (model.includes('gpt-5.6-terra')) return 1;
  if (model.includes('gpt-5.6-luna')) return 2;
  return 3;
};

const modelVersion = (key: string): number => {
  const model = key.toLowerCase();
  const match = model.match(/gpt[-_ ]?(\d+(?:\.\d+)?)/) ?? model.match(/\bo(\d+(?:\.\d+)?)/) ?? model.match(/(\d+(?:\.\d+)?)/);
  return match ? Number(match[1]) : -1;
};

const sortCostUserStackKeys = (keys: string[]): string[] => [...keys].sort((a, b) => {
  if (a === OTHERS_STACK_KEY || b === OTHERS_STACK_KEY) return a === OTHERS_STACK_KEY ? 1 : -1;

  const familyDiff = stackFamilyRank(a) - stackFamilyRank(b);
  if (familyDiff !== 0) return familyDiff;

  if (stackFamilyRank(a) === 0) {
    const tierDiff = claudeTierRank(a) - claudeTierRank(b);
    if (tierDiff !== 0) return tierDiff;
  }

  if (stackFamilyRank(a) === 1) {
    const versionDiff = modelVersion(b) - modelVersion(a);
    if (versionDiff !== 0) return versionDiff;

    const tierDiff = codexTierRank(a) - codexTierRank(b);
    if (tierDiff !== 0) return tierDiff;
  }

  const versionDiff = modelVersion(b) - modelVersion(a);
  if (versionDiff !== 0) return versionDiff;

  return a.localeCompare(b);
});

// Usage with no user_id is money spent all the same, so it keeps its own bar and
// its share of the totals. It gets a label a reader can read, and one that no
// real user_id reaches: keyed by the raw string 'unknown', it used to merge with
// a user whose id was literally that.
const UNKNOWN_USER_LABEL = 'Unknown user';

const userLabel = (userID: string | undefined, nameMap: Record<string, string>): string => {
  if (!userID) return UNKNOWN_USER_LABEL;
  const name = nameMap[userID];
  if (name) return name;
  if (userID.length > 16 && /^[0-9a-f]+$/i.test(userID)) return userID.slice(0, 8);
  return userID;
};

const rowValue = (row: CostUserStackRow, viewMode: BuildCostUserStackDataParams['viewMode']): number => {
  if (viewMode === 'token') return row.total_input_tokens + row.total_output_tokens;
  return row.total_cost;
};

const modelRowValue = (row: CostModelRow, viewMode: BuildCostModelDataParams['viewMode']): number => {
  if (viewMode === 'token') return row.input_tokens + row.output_tokens;
  return row.total_cost;
};

const displayValue = (value: number, viewMode: BuildCostModelDataParams['viewMode']): number => {
  if (viewMode === 'token') return Math.round(value);
  return parseFloat(value.toFixed(4));
};

const rowTotal = (row: CostUserStackDatum): number =>
  Object.entries(row)
    .filter(([key]) => key !== 'name')
    .reduce((sum, [, value]) => sum + Number(value), 0);

const buildCostUserStackData = ({ rows, nameMap, viewMode }: BuildCostUserStackDataParams): CostUserStackResult => {
  const data = Object.values(
    rows.reduce<Record<string, CostUserStackDatum>>(
      (acc, row) => {
        const user = userLabel(row.user_id, nameMap);
        const key = costUserStackKey(row.model);
        const value = rowValue(row, viewMode);

        if (!acc[user]) acc[user] = { name: user };
        acc[user][key] = Number(acc[user][key] ?? 0) + value;

        return acc;
      },
      {},
    ),
  ).sort((a, b) => rowTotal(b) - rowTotal(a));

  const stackKeys = sortCostUserStackKeys(
    Object.keys(
      data.reduce<Record<string, boolean>>(
        (acc, row) => {
          for (const key of Object.keys(row)) {
            if (key !== 'name' && Number(row[key] ?? 0) > 0) acc[key] = true;
          }
          return acc;
        },
        {},
      ),
    ),
  );

  return { data, stackKeys };
};

const buildCostModelData = ({ rows, viewMode }: BuildCostModelDataParams): CostModelResult => {
  const modelAgg: Record<string, number> = {};
  const otherNames = new Set<string>();

  for (const row of rows) {
    const key = costUserStackKey(row.model);
    const value = modelRowValue(row, viewMode);
    modelAgg[key] = (modelAgg[key] ?? 0) + value;

    if (key === OTHERS_STACK_KEY) {
      otherNames.add(shortModel(row.model));
    }
  }

  const data = sortCostUserStackKeys(Object.keys(modelAgg).filter((key) => modelAgg[key] > 0))
    .map((key) => ({
      name: key,
      value: displayValue(modelAgg[key], viewMode),
    }));

  return {
    data,
    otherNames: [...otherNames].sort((a, b) => a.localeCompare(b)),
  };
};

export { buildCostModelData, buildCostUserStackData, costUserStackKey };
export type { CostModelDatum, CostUserStackDatum, CostUserStackKey };

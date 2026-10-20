import { describe, expect, it } from 'vitest';
import type { AnalysisRepoStatus, FrameAccount, SeriesBand, SeriesPoint, UsageWindowSeries } from '@/lib/session-analysis-types';
import {
  chartLabel,
  chartPaths,
  chartSamples,
  clampPct,
  identityKey,
  meterLevel,
  meterRows,
  pctText,
  repoStatusText,
  repoStatusFromAnalysis,
  repoStatusDescription,
  resetText,
  type ChartGeometry,
} from './model';

const NOW = Date.parse('2026-10-07T06:40:00Z');
const MIN = 60_000;
const at = (minutes: number): string => new Date(NOW + minutes * MIN).toISOString();

describe('meterLevel: 60 미만 low, 85 미만 mid, 그 이상 high', () => {
  it('경계값', () => {
    expect(meterLevel(0)).toBe('low');
    expect(meterLevel(59.9)).toBe('low');
    expect(meterLevel(60)).toBe('mid');
    expect(meterLevel(84.9)).toBe('mid');
    expect(meterLevel(85)).toBe('high');
    expect(meterLevel(100)).toBe('high');
  });

  it('모르면 unknown이다(low가 아니다)', () => {
    expect(meterLevel(null)).toBe('unknown');
    expect(meterLevel(Number.NaN)).toBe('unknown');
    expect(meterLevel(Number.POSITIVE_INFINITY)).toBe('unknown');
  });
});

describe('pctText: 모름은 0%가 아니다', () => {
  it('관측한 값은 반올림한 퍼센트, 0은 0%', () => {
    expect(pctText(99)).toBe('99%');
    expect(pctText(16.4)).toBe('16%');
    expect(pctText(0)).toBe('0%');
    expect(pctText(99.6)).toBe('100%');
  });

  it('0보다 크고 1보다 작으면 "<1%"(0으로 보이지 않게)', () => {
    expect(pctText(0.4)).toBe('<1%');
    expect(pctText(0.99)).toBe('<1%');
    expect(pctText(1)).toBe('1%');
  });

  it('null·NaN은 "?"이고 "0%"가 아니다', () => {
    expect(pctText(null)).toBe('?');
    expect(pctText(Number.NaN)).toBe('?');
    expect(pctText(null)).not.toContain('0');
  });

  it('범위를 넘으면 0~100으로 가둔다', () => {
    expect(pctText(140)).toBe('100%');
    expect(pctText(-5)).toBe('0%');
    expect(clampPct(140)).toBe(100);
    expect(clampPct(-5)).toBe(0);
  });
});

describe('resetText', () => {
  it('시각을 모르거나 읽을 수 없으면 그렇게 적는다', () => {
    expect(resetText(null, NOW)).toBe('재설정 시각을 알 수 없음');
    expect(resetText('not-a-time', NOW)).toBe('재설정 시각을 알 수 없음');
  });

  it('지났으면 "재설정됨"(같은 시각 포함)', () => {
    expect(resetText(at(-10), NOW)).toBe('재설정됨');
    expect(resetText(at(0), NOW)).toBe('재설정됨');
  });

  it('1분 미만, 분, 시간, 일', () => {
    expect(resetText(new Date(NOW + 30_000).toISOString(), NOW)).toBe('1분 안에 재설정');
    expect(resetText(at(45), NOW)).toBe('45분 뒤 재설정');
    expect(resetText(at(130), NOW)).toBe('2시간 10분 뒤 재설정');
    expect(resetText(at(180), NOW)).toBe('3시간 뒤 재설정');
    expect(resetText(at(60 * 24 * 5 + 60 * 3), NOW)).toBe('5일 3시간 뒤 재설정');
    expect(resetText(at(60 * 24 * 2), NOW)).toBe('2일 뒤 재설정');
  });

  it('남은 시간은 올림으로 센다(2시간 9분 31초는 2시간 10분)', () => {
    expect(resetText(new Date(NOW + (129 * 60 + 31) * 1000).toISOString(), NOW)).toBe('2시간 10분 뒤 재설정');
    // 59분 30초는 60분이 되어 "1시간"이다(60분 뒤 재설정이라고 적지 않는다).
    expect(resetText(new Date(NOW + (59 * 60 + 30) * 1000).toISOString(), NOW)).toBe('1시간 뒤 재설정');
  });
});

const account = (over: Partial<FrameAccount> = {}): FrameAccount => ({
  label: '서브',
  provider: 'claude',
  five_hour_pct: 99,
  seven_day_pct: 16,
  resets_at: { five_hour: at(130), seven_day: at(60 * 24 * 3) },
  selected: true,
  ...over,
});

describe('meterRows: 위 줄 5시간, 아래 줄 7일', () => {
  it('값, 수준, 문구, 툴팁 문장', () => {
    const [five, seven] = meterRows(account(), NOW);
    expect(five).toMatchObject({ window: 'five_hour', label: '5시간', pct: 99, level: 'high', text: '99%' });
    expect(five.title).toBe('5시간 창 99% · 2시간 10분 뒤 재설정');
    expect(seven).toMatchObject({ window: 'seven_day', label: '7일', pct: 16, level: 'low', text: '16%' });
    expect(seven.title).toBe('7일 창 16% · 3일 뒤 재설정');
  });

  it('관측한 0은 0%다', () => {
    const [five] = meterRows(account({ five_hour_pct: 0 }), NOW);
    expect(five).toMatchObject({ pct: 0, level: 'low', text: '0%' });
  });

  it('모르는 사용률은 null이고 문구는 "?", 툴팁은 모른다고 적는다(0이 아니다)', () => {
    const [five, seven] = meterRows(account({ five_hour_pct: null, seven_day_pct: Number.NaN }), NOW);
    for (const row of [five, seven]) {
      expect(row.pct).toBeNull();
      expect(row.level).toBe('unknown');
      expect(row.text).toBe('?');
      expect(row.title).toContain('사용률을 알 수 없음');
      expect(row.title).not.toMatch(/(^|\D)0%/);
    }
  });

  it('재설정 시각을 모르면 툴팁이 그렇게 적는다', () => {
    const [five] = meterRows(account({ resets_at: { five_hour: null, seven_day: null } }), NOW);
    expect(five.title).toBe('5시간 창 99% · 재설정 시각을 알 수 없음');
  });

  it('범위를 넘는 값은 0~100으로 가둔다', () => {
    const [five, seven] = meterRows(account({ five_hour_pct: 140, seven_day_pct: -3 }), NOW);
    expect(five.pct).toBe(100);
    expect(seven.pct).toBe(0);
  });
});

describe('identityKey: 같은 Claude 계정끼리 색이 겹치지 않는다', () => {
  const list = [
    account({ label: 'a' }),
    account({ label: 'b' }),
    account({ label: 'c', provider: 'codex' }),
    account({ label: 'd' }),
  ];

  it('Claude는 순서대로 자리를 받고, Codex는 자기 색이다', () => {
    expect(list.map((_, i) => identityKey(list, i))).toEqual(['slot-0', 'slot-1', 'codex', 'slot-2']);
  });

  it('네 자리가 차면 처음으로 돌아온다', () => {
    const many = Array.from({ length: 5 }, (_, i) => account({ label: `n${i}` }));
    expect(identityKey(many, 4)).toBe('slot-0');
  });
});

/** 계산하기 쉬운 기하: x = 비율 × 100, y = 50 − 사용률 / 2. */
const G: ChartGeometry = { width: 100, height: 50, padX: 0, padY: 0 };

const series = (over: Partial<UsageWindowSeries> = {}): UsageWindowSeries => ({
  window: 'five_hour',
  current: [],
  past: [],
  median: [],
  band: [],
  ...over,
});

const pt = (x: number, y: number): SeriesPoint => ({ x, y });

describe('chartPaths', () => {
  it('선: 가로는 창이 지난 비율, 세로는 아래에서 위로 사용률', () => {
    const paths = chartPaths(
      series({
        current: [pt(0, 0), pt(0.5, 50), pt(1, 100)],
        past: [[pt(0, 10), pt(1, 40)], []],
        median: [pt(0, 20), pt(1, 60)],
      }),
      G,
    );
    expect(paths.current).toBe('M0 50 L50 25 L100 0');
    expect(paths.past).toEqual(['M0 45 L100 30']);
    expect(paths.median).toBe('M0 40 L100 20');
    expect(paths).toMatchObject({ hasCurrent: true, hasPast: true, hasMedian: true, hasBand: false });
  });

  it('25~75% 범위는 위 가장자리를 따라갔다가 아래 가장자리로 돌아오는 닫힌 다각형', () => {
    const band: SeriesBand[] = [
      { x: 0, low: 10, high: 30 },
      { x: 1, low: 20, high: 60 },
    ];
    const paths = chartPaths(series({ band }), G);
    expect(paths.band).toBe('M0 35 L100 20 L100 40 L0 45 Z');
    expect(paths.hasBand).toBe(true);
  });

  it('low와 high가 뒤집혀 와도 같은 다각형이다', () => {
    const swapped: SeriesBand[] = [
      { x: 0, low: 30, high: 10 },
      { x: 1, low: 60, high: 20 },
    ];
    expect(chartPaths(series({ band: swapped }), G).band).toBe('M0 35 L100 20 L100 40 L0 45 Z');
  });

  it('비었으면 빈 문자열과 거짓 플래그(깨지지 않는다)', () => {
    const paths = chartPaths(series(), G);
    expect(paths).toEqual({
      past: [],
      median: '',
      band: '',
      current: '',
      hasPast: false,
      hasMedian: false,
      hasBand: false,
      hasCurrent: false,
    });
  });

  it('점이 하나면 길이 0의 선분(둥근 끝이 점으로 보인다), 범위는 점 둘이 있어야 한다', () => {
    const paths = chartPaths(series({ current: [pt(0.5, 50)], band: [{ x: 0.5, low: 10, high: 30 }] }), G);
    expect(paths.current).toBe('M50 25 L50 25');
    expect(paths.band).toBe('');
    expect(paths.hasBand).toBe(false);
  });

  it('x 순서가 뒤섞여 와도 정렬해서 그린다', () => {
    expect(chartPaths(series({ current: [pt(1, 100), pt(0, 0), pt(0.5, 50)] }), G).current).toBe('M0 50 L50 25 L100 0');
  });

  it('유한하지 않은 점은 건너뛴다', () => {
    const paths = chartPaths(
      series({ current: [pt(Number.NaN, 5), pt(0, 0), pt(1, Number.POSITIVE_INFINITY), pt(1, 100)] }),
      G,
    );
    expect(paths.current).toBe('M0 50 L100 0');
  });

  it('범위를 넘는 값은 그림 안쪽으로 가둔다', () => {
    expect(chartPaths(series({ current: [pt(-1, -20), pt(2, 140)] }), G).current).toBe('M0 50 L100 0');
  });

  it('소수점 한 자리로 반올림한다', () => {
    const g: ChartGeometry = { width: 120, height: 30, padX: 2, padY: 3 };
    // x = 2 + 0.333 × 116 = 40.628, y = 27 − 0.333 × 24 = 19.008
    expect(chartPaths(series({ current: [pt(0.333, 33.3)] }), g).current).toBe('M40.6 19 L40.6 19');
  });

  it('모든 좌표가 여백 안쪽에 있다(범위 밖 입력 포함)', () => {
    const g: ChartGeometry = { width: 120, height: 30, padX: 2, padY: 3 };
    const wild = [pt(-0.4, -30), pt(0.2, 12.5), pt(0.7, 99.9), pt(1.6, 250)];
    const paths = chartPaths(
      series({
        current: wild,
        past: [wild, [pt(0, 0), pt(1, 100)]],
        median: wild,
        band: [
          { x: -1, low: -10, high: 20 },
          { x: 2, low: 80, high: 300 },
        ],
      }),
      g,
    );
    const all = [paths.current, paths.median, paths.band, ...paths.past];
    for (const d of all) {
      const nums = (d.match(/-?\d+(?:\.\d+)?/g) ?? []).map(Number);
      expect(nums.length % 2).toBe(0);
      for (let i = 0; i < nums.length; i += 2) {
        expect(nums[i]).toBeGreaterThanOrEqual(g.padX);
        expect(nums[i]).toBeLessThanOrEqual(g.width - g.padX);
        expect(nums[i + 1]).toBeGreaterThanOrEqual(g.padY);
        expect(nums[i + 1]).toBeLessThanOrEqual(g.height - g.padY);
      }
    }
  });

  it('입력 배열을 바꾸지 않는다', () => {
    const current = [pt(1, 100), pt(0, 0)];
    chartPaths(series({ current }), G);
    expect(current).toEqual([pt(1, 100), pt(0, 0)]);
  });

  it('n=2는 범위 면을 분리하고 표본 0은 중앙값 선을 끊는다', () => {
    const lowSample = chartPaths(series({
      median: [pt(0, 10), pt(1, 90)],
      band: [
        { x: 0, low: 5, high: 15 },
        { x: 0.25, low: 20, high: 30 },
        { x: 0.75, low: 70, high: 80 },
        { x: 1, low: 85, high: 95 },
      ],
      sampleCounts: [{ x: 0.5, n: 2 }],
    }), G);
    expect(lowSample.median.match(/M/g)).toHaveLength(1);
    expect(lowSample.band.match(/M/g)).toHaveLength(2);
    expect(lowSample.band.match(/Z/g)).toHaveLength(2);

    const emptySample = chartPaths(series({ median: [pt(0, 10), pt(1, 90)], sampleCounts: [{ x: 0.5, n: 0 }] }), G);
    expect(emptySample.median.match(/M/g)).toHaveLength(2);
  });
});

describe('chartSamples', () => {
  it('서로 다른 시간 간격에서도 관측되지 않은 값을 보간하지 않는다', () => {
    const samples = chartSamples(series({
      current: [pt(0, 0), pt(0.5, 50)],
      median: [pt(0, 20), pt(1, 60)],
      past: [[pt(0, 10), pt(1, 30)]],
      band: [{ x: 0, low: 10, high: 30 }, { x: 1, low: 20, high: 60 }],
    }));
    expect(samples.map((sample) => sample.x)).toEqual([0, 0.5, 1]);
    expect(samples[1]).toEqual({ x: 0.5, current: 50, median: null, low: null, high: null, past: [null], n: null });
    expect(samples[2].current).toBeNull();
  });

  it('비유한값을 건너뛰고 관측한 0과 관측하지 않은 값을 구별한다', () => {
    const samples = chartSamples(series({ current: [pt(0, 0), pt(Number.NaN, 5)], past: [[]] }));
    expect(samples).toEqual([{ x: 0, current: 0, median: null, low: null, high: null, past: [null], n: null }]);
  });

  it('정렬된 API 버킷의 결측값과 n<4 범위는 보간하지 않는다', () => {
    const samples = chartSamples(series({
      median: [pt(0, 10), pt(1, 90)],
      band: [{ x: 0, low: 5, high: 15 }, { x: 1, low: 85, high: 95 }],
      sampleCounts: [{ x: 0, n: 4 }, { x: 0.5, n: 0 }, { x: 1, n: 2 }],
    }));
    expect(samples.map(({ x, n, median, low, high }) => ({ x, n, median, low, high }))).toEqual([
      { x: 0, n: 4, median: 10, low: 5, high: 15 },
      { x: 0.5, n: 0, median: null, low: null, high: null },
      { x: 1, n: 2, median: 90, low: null, high: null },
    ]);
  });

  it('표본 4개 미만인 범위는 차트 접근성 설명에서 판단 불가로 알린다', () => {
    const label = chartLabel(series({
      past: [[pt(0, 10)]],
      sampleCounts: [{ x: 0.5, n: 2 }],
    }), '서브');
    expect(label).toContain('25~75%');
    expect(label).toContain('판단 불가');
  });

  it('요약 분위수와 이력 수가 있으면 raw past가 비어도 과거 자료 없음이라 하지 않는다', () => {
    const label = chartLabel(series({
      historyCount: 2,
      median: [pt(0.5, 30)],
      sampleCounts: [{ x: 0.5, n: 2 }],
    }), '서브');
    expect(label).toContain('지난 창 2개');
    expect(label).toContain('25~75%');
    expect(label).not.toContain('비교할 지난 창이 없습니다');
  });
});

describe('chartLabel', () => {
  it('창 이름, 지금 창의 마지막 값(가장 늦은 점), 지난 창의 수를 말한다', () => {
    const label = chartLabel(
      series({ current: [pt(0.9, 17), pt(0.2, 5)], past: Array.from({ length: 36 }, () => [pt(0, 0)]) }),
      '서브',
    );
    expect(label).toContain('서브 5시간 창 사용률');
    expect(label).toContain('지금 창은 17%입니다');
    expect(label).toContain('지난 창 36개');
  });

  it('7일 창', () => {
    expect(chartLabel(series({ window: 'seven_day' }), '메인')).toContain('메인 7일 창 사용률');
  });

  it('값이 없으면 없다고 적는다(0%가 아니다)', () => {
    const label = chartLabel(series(), '서브');
    expect(label).toContain('지금 창의 값이 없습니다');
    expect(label).toContain('비교할 지난 창이 없습니다');
    expect(label).not.toContain('0%');
  });

  it('계정 이름이 없으면 맨 앞에 공백이 붙지 않는다', () => {
    expect(chartLabel(series(), '').startsWith('5시간 창 사용률')).toBe(true);
  });
});

describe('repoStatusText', () => {
  it('미커밋 N건은 dirty', () => {
    expect(repoStatusText({ branch: 'master', uncommitted: 5 })).toEqual({ branch: 'master', uncommitted: '미커밋 5건', tone: 'dirty' });
    expect(repoStatusText({ branch: 'master', uncommitted: 1234 }).uncommitted).toBe('미커밋 1,234건');
  });

  it('관측한 0은 "미커밋 없음"(clean)', () => {
    expect(repoStatusText({ branch: 'main', uncommitted: 0 })).toEqual({ branch: 'main', uncommitted: '미커밋 없음', tone: 'clean' });
  });

  it('모르면 "알 수 없음"(unknown)이고 없음이 아니다', () => {
    expect(repoStatusText({ branch: 'main', uncommitted: null })).toEqual({
      branch: 'main',
      uncommitted: '미커밋 알 수 없음',
      tone: 'unknown',
    });
    expect(repoStatusText({ branch: 'main', uncommitted: Number.NaN }).tone).toBe('unknown');
    expect(repoStatusText({ branch: 'main', uncommitted: -1 }).tone).toBe('unknown');
  });

  it('브랜치가 비면 그렇게 적는다', () => {
    expect(repoStatusText({ branch: '  ', uncommitted: 0 }).branch).toBe('브랜치 없음');
  });

  it('P4 owner 신호의 관측 수치와 unknown을 FrameStatusBar 입력으로 보존한다', () => {
    const observed: AnalysisRepoStatus = {
      branch: 'feature/session', branch_state: 'observed', branch_source: 'session_record_git_branch',
      uncommitted_edited_files: 0, uncommitted_state: 'observed', uncommitted_source: 'session_git_snapshot',
      uncommitted_scope: 'session_edited_files', uncommitted_reason: null, changed_at: '2026-10-09T08:00:00Z',
    };
    expect(repoStatusFromAnalysis(observed)).toEqual({
      branch: 'feature/session', uncommitted: 0, source: 'session_git_snapshot',
      scope: 'session_edited_files', reason: null,
    });
    expect(repoStatusDescription(repoStatusFromAnalysis(observed))).toBe('출처: 세션 Git 스냅샷 · 범위: 이 세션 편집 파일');
    expect(repoStatusFromAnalysis(undefined)).toEqual({
      branch: null, uncommitted: null, source: 'unknown', scope: undefined, reason: undefined,
    });
    expect(repoStatusText(repoStatusFromAnalysis(undefined))).toEqual({
      branch: '브랜치 알 수 없음', uncommitted: '미커밋 알 수 없음', tone: 'unknown',
    });
  });
});

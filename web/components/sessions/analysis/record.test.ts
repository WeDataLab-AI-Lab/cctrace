import { describe, expect, it } from 'vitest';
import type { RecordState, SessionRecord } from '@/lib/session-analysis-types';
import { KIND_TAG_LABEL, RECORD_NOTICE, RECORD_STATE_WORD, REVIEW_PREFIX, hasRecordContent, kindTagLabel, recordNotice, reviewPrefix, splitInlineCode } from './record';

describe('인라인 코드 나누기', () => {
  it('백틱으로 감싼 부분만 코드다', () => {
    expect(splitInlineCode('읽음은 `inbox_reads`에 저장한다')).toEqual([
      { code: false, text: '읽음은 ' },
      { code: true, text: 'inbox_reads' },
      { code: false, text: '에 저장한다' },
    ]);
  });

  it('코드가 여럿이어도, 처음과 끝에 와도 나눈다', () => {
    expect(splitInlineCode('`a` 와 `b`')).toEqual([
      { code: true, text: 'a' },
      { code: false, text: ' 와 ' },
      { code: true, text: 'b' },
    ]);
  });

  it('짝이 없는 백틱은 글자 그대로 둔다(내용을 버리지 않는다)', () => {
    expect(splitInlineCode('따옴표 ` 하나만')).toEqual([{ code: false, text: '따옴표 ` 하나만' }]);
    expect(splitInlineCode('`열고 닫지 않음')).toEqual([{ code: false, text: '`열고 닫지 않음' }]);
  });

  it('빈 코드(``)는 코드로 치지 않고 글자로 남긴다', () => {
    expect(splitInlineCode('앞``뒤').map((s) => s.text).join('')).toBe('앞``뒤');
    expect(splitInlineCode('앞``뒤').every((s) => !s.code)).toBe(true);
  });

  it('백틱이 없으면 한 덩어리, 빈 글은 빈 배열', () => {
    expect(splitInlineCode('그냥 글')).toEqual([{ code: false, text: '그냥 글' }]);
    expect(splitInlineCode('')).toEqual([]);
  });

  it('나눈 조각을 이으면 백틱 빼고 원문과 같다', () => {
    const text = '앞 `x` 중간 `y/z` 뒤';
    expect(splitInlineCode(text).map((s) => s.text).join('')).toBe(text.replaceAll('`', ''));
  });
});

describe('세션 기록의 상태', () => {
  const states: RecordState[] = ['ready', 'stale', 'generating', 'none', 'withheld', 'redacted', 'failed', 'disabled', 'missing', 'blocked', 'waiting_budget'];

  it('본문이 있는 것은 ready와 stale뿐이다', () => {
    const withContent = states.filter((state) => {
      const record: SessionRecord =
        state === 'ready' || state === 'stale'
          ? { state, generated_at: null, covers_until: null, headline: 'h', bullets: [], review: [], tags: { kinds: [], components: [], screens: [], related: [] }, work: [] }
          : { state, generated_at: null, covers_until: null };
      return hasRecordContent(record);
    });
    expect(withContent).toEqual(['ready', 'stale']);
    expect(hasRecordContent(undefined)).toBe(false);
  });

  it('본문이 없는 다섯 상태는 각자 안내가 있고, 기록이 없다고 작업이 없었다는 뜻이 아니라고 하거나 이유를 적는다', () => {
    for (const state of ['generating', 'none', 'withheld', 'redacted', 'failed', 'disabled', 'missing', 'blocked', 'waiting_budget'] as const) {
      const notice = RECORD_NOTICE[state];
      expect(notice.title.length, state).toBeGreaterThan(5);
      expect(notice.body.length, state).toBeGreaterThan(20);
    }
    expect(RECORD_NOTICE.withheld.body).toContain('작업이 없었다는 뜻이 아닙니다');
    expect(RECORD_NOTICE.redacted.body).toContain('비식별화');
    expect(RECORD_NOTICE.failed.tone).toBe('danger');
    expect(RECORD_NOTICE.generating.tone).toBe('pending');
  });

  it('가려진 상태는 recap으로 대신하지 않고, 나머지는 recap을 대신 보인다', () => {
    expect(RECORD_NOTICE.redacted.recapFallback).toBe(false);
    for (const state of ['generating', 'none', 'withheld', 'failed'] as const) expect(RECORD_NOTICE[state].recapFallback).toBe(true);
  });

  it('모르는 상태는 null이고 상태 이름은 일곱 개가 모두 있다', () => {
    expect(recordNotice('paused')).toBeNull();
    expect(recordNotice('none')?.title).toContain('아직 세션 기록이 없습니다');
    expect(Object.keys(RECORD_STATE_WORD).sort()).toEqual([...states].sort());
  });

  it('API reason이 같은 state 안의 차이를 설명하고 실패 코드를 보존한다', () => {
    expect(recordNotice('withheld', 'disabled')?.reasonDescription).toContain('관리자가 세션 기록 생성을 껐');
    expect(recordNotice('withheld', 'consent_required')?.reasonDescription).toContain('동의');
    expect(recordNotice('none', 'waiting_budget')?.reasonDescription).toContain('일일 토큰 한도');
    expect(recordNotice('none', 'facts_missing')?.reasonDescription).toContain('분석 사실');
    expect(recordNotice('failed', 'schema_violation')?.reasonDescription).toContain('근거 검증');
    expect(recordNotice('failed', 'schema_violation')?.reason).toBe('schema_violation');
  });
});

describe('태그와 검토 사항의 이름', () => {
  it('종별은 구현·조사·정비·상담·버그 수정·문서', () => {
    expect(Object.values(KIND_TAG_LABEL)).toEqual(['구현', '조사', '정비', '상담', '버그 수정', '문서']);
    expect(kindTagLabel('implementation')).toBe('구현');
    expect(kindTagLabel('new_kind')).toBe('new_kind');
  });

  it('검토 사항의 접두는 결정·해석·미확인', () => {
    expect(REVIEW_PREFIX).toEqual({ decision: '결정', interpretation: '해석', unverified: '미확인' });
    expect(reviewPrefix('decision')).toBe('결정');
    expect(reviewPrefix('other')).toBe('other');
  });
});

import { describe, expect, it } from 'vitest';
import { FIELD_ORDER, searchSessions, type SearchDoc } from './search';

const doc = (over: Partial<SearchDoc> & { id: string }): SearchDoc => ({
  title: null,
  headline: null,
  work: [],
  commits: [],
  tasks: [],
  note: null,
  ...over,
});

const DOCS: SearchDoc[] = [
  doc({
    id: 'a',
    title: '정산 리포트 내보내기 화면',
    headline: '정산 화면에 CSV 내보내기를 더했다',
    work: ['정산 목록에 내보내기 버튼을 더해 주세요', '파일 이름 규칙을 정한다'],
    commits: ['1105386 정산 목록에 내보내기 버튼', 'c4483a7'],
    tasks: ['task-123', 'feature-107'],
    note: '내일 리뷰 요청',
  }),
  doc({ id: 'b', title: '결제 웹훅 재시도 정리', work: ['중복 처리를 막는다'], tasks: ['task-9'] }),
  doc({ id: 'c', title: null, headline: null, note: 'task-123 후속' }),
];

describe('searchSessions', () => {
  it('검색어가 비었거나 공백이면 null이다(거르지 않는다는 뜻, 빈 배열이 아니다)', () => {
    expect(searchSessions(DOCS, '')).toBeNull();
    expect(searchSessions(DOCS, '   ')).toBeNull();
    expect(searchSessions(DOCS, '\t\n')).toBeNull();
  });

  it('맞는 것이 없으면 빈 배열이다', () => {
    expect(searchSessions(DOCS, '존재하지 않는 낱말')).toEqual([]);
  });

  it('대소문자를 가리지 않는 부분 일치', () => {
    expect(searchSessions(DOCS, 'TASK-12')?.map((h) => h.id)).toEqual(['a', 'c']);
    expect(searchSessions(DOCS, 'csv')?.map((h) => h.id)).toEqual(['a']);
  });

  it('낱말은 모두(AND) 맞아야 하고, 서로 다른 필드에서 맞아도 된다', () => {
    const hits = searchSessions(DOCS, '정산 task-123');
    expect(hits).toEqual([{ id: 'a', fields: ['title', 'headline', 'work', 'commits', 'tasks'] }]);
    // 둘 다 가진 세션만 남는다: c는 task-123만, b는 어느 쪽도 아니다.
    expect(searchSessions(DOCS, '결제 task-123')).toEqual([]);
  });

  it('맞은 필드를 정해진 순서로 알려 준다(낱말 순서와 무관)', () => {
    const asc = searchSessions(DOCS, '내보내기');
    const same = searchSessions(DOCS, '내보내기 정산');
    expect(asc?.[0].fields).toEqual(['title', 'headline', 'work', 'commits']);
    expect(same?.[0].fields).toEqual(['title', 'headline', 'work', 'commits']);
    const order = same?.[0].fields.map((f) => FIELD_ORDER.indexOf(f)) ?? [];
    expect([...order].sort((x, y) => x - y)).toEqual(order);
  });

  it('메모와 task 필드', () => {
    expect(searchSessions(DOCS, '리뷰')).toEqual([{ id: 'a', fields: ['note'] }]);
    expect(searchSessions(DOCS, 'feature-107')).toEqual([{ id: 'a', fields: ['tasks'] }]);
  });

  it('결과는 입력 순서를 지킨다', () => {
    const reversed = [...DOCS].reverse();
    expect(searchSessions(reversed, 'task-123')?.map((h) => h.id)).toEqual(['c', 'a']);
  });

  it('제목이나 요약이 null이어도 깨지지 않는다', () => {
    expect(searchSessions([doc({ id: 'x' })], '아무거나')).toEqual([]);
  });

  it('한글을 풀어 쓴 꼴(NFD)로 와도 같은 글자로 맞는다', () => {
    const nfd = '정산 리포트'.normalize('NFD');
    expect(searchSessions(DOCS, nfd)?.map((h) => h.id)).toEqual(['a']);
    const decomposedDoc = [doc({ id: 'd', title: '정산 리포트'.normalize('NFD') })];
    expect(searchSessions(decomposedDoc, '정산')?.map((h) => h.id)).toEqual(['d']);
  });
});

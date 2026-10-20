/**
 * 전역 검색(A4)의 순수 로직. 세션 제목·한 줄 요약·작업 항목·커밋·task·메모를 가로질러 찾는다.
 *
 * 규칙: 검색어를 공백으로 나눈 낱말을 모두(AND) 가진 세션만 남기고, 낱말마다 대소문자를 가리지 않는 부분 일치다.
 * 한 낱말은 필드 어디서든 맞으면 된다. 낱말이 서로 다른 필드에서 맞아도 된다.
 */

interface SearchDoc {
  id: string;
  title: string | null;
  headline: string | null;
  work: readonly string[];
  commits: readonly string[];
  tasks: readonly string[];
  note: string | null;
}

type SearchField = 'title' | 'headline' | 'work' | 'commits' | 'tasks' | 'note';

interface SearchHit {
  id: string;
  /** 맞은 필드. 아래 FIELD_ORDER 순서로 고정이다. */
  fields: SearchField[];
}

/** 결과의 `fields` 순서. 입력 순서나 낱말 순서에 따라 바뀌지 않는다. */
const FIELD_ORDER: readonly SearchField[] = ['title', 'headline', 'work', 'commits', 'tasks', 'note'];

/** macOS 파일 이름은 한글을 풀어 쓴 꼴(NFD)로 올 수 있어, 양쪽을 같은 꼴(NFC)로 맞춰 비교한다. */
const norm = (s: string): string => s.normalize('NFC').toLowerCase();

const textsOf = (doc: SearchDoc, field: SearchField): readonly string[] => {
  switch (field) {
    case 'title':
      return doc.title ? [doc.title] : [];
    case 'headline':
      return doc.headline ? [doc.headline] : [];
    case 'work':
      return doc.work;
    case 'commits':
      return doc.commits;
    case 'tasks':
      return doc.tasks;
    case 'note':
      return doc.note ? [doc.note] : [];
  }
};

/**
 * 검색어가 비었으면 null: "거르지 않는다"는 뜻이다(빈 배열은 "맞는 것이 없다"). 아니면 맞은 세션만 입력 순서대로 돌려준다.
 */
const searchSessions = (docs: readonly SearchDoc[], query: string): SearchHit[] | null => {
  const terms = norm(query)
    .split(/\s+/)
    .filter((t) => t !== '');
  if (terms.length === 0) return null;

  const hits: SearchHit[] = [];
  for (const doc of docs) {
    const haystack = new Map<SearchField, string[]>(FIELD_ORDER.map((f) => [f, textsOf(doc, f).map(norm)]));
    const matched = new Set<SearchField>();
    let all = true;
    for (const term of terms) {
      let found = false;
      for (const field of FIELD_ORDER) {
        if (haystack.get(field)?.some((text) => text.includes(term))) {
          matched.add(field);
          found = true;
        }
      }
      if (!found) {
        all = false;
        break;
      }
    }
    if (all) hits.push({ id: doc.id, fields: FIELD_ORDER.filter((f) => matched.has(f)) });
  }
  return hits;
};

export { searchSessions, FIELD_ORDER };
export type { SearchDoc, SearchField, SearchHit };

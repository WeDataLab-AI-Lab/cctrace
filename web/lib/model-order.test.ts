import { describe, it, expect } from 'vitest';
import { compareModelLabels } from './model-order';

const sorted = (labels: string[]) => [...labels].sort(compareModelLabels);

describe('compareModelLabels', () => {
  it('계열 순서는 claude → codex → compat 로 고정된다', () => {
    expect(sorted(['gemma4:12b', 'gpt-5.5', 'sonnet 5'])).toEqual([
      'sonnet 5', 'gpt-5.5', 'gemma4:12b',
    ]);
  });

  it('claude 계열은 fable → opus → sonnet → haiku 티어 순이다', () => {
    expect(sorted(['haiku 4.5', 'sonnet 5', 'opus 5', 'fable 5'])).toEqual([
      'fable 5', 'opus 5', 'sonnet 5', 'haiku 4.5',
    ]);
  });

  it('같은 티어 안에서는 버전 내림차순이다', () => {
    expect(sorted(['opus 4.5', 'opus 5', 'opus 4.8', 'opus 4.6'])).toEqual([
      'opus 5', 'opus 4.8', 'opus 4.6', 'opus 4.5',
    ]);
  });

  it('버전은 문자열이 아니라 세그먼트 단위 숫자로 비교한다', () => {
    expect(sorted(['opus 4.9', 'opus 4.10'])).toEqual(['opus 4.10', 'opus 4.9']);
  });

  it('codex 계열은 버전 내림차순이고 같은 버전 안에서 sol → terra → luna → mini 다', () => {
    expect(sorted([
      'gpt-5.2', 'gpt-5.6-terra', 'gpt-5.6-luna', 'gpt-5.5', 'gpt-5.1-codex-max', 'gpt-5.6-sol',
    ])).toEqual([
      'gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna', 'gpt-5.5', 'gpt-5.2', 'gpt-5.1-codex-max',
    ]);
  });

  // gpt-6-astra는 gpt-5.6 세대 전체를 잇는 새 플래그십이다. codex는 버전이 티어보다
  // 먼저이므로, 세대 번호(6 > 5.x)만으로 sol을 포함한 모든 gpt-5.x 위에 서야 한다.
  it('gpt-6-astra는 새 세대라 sol을 포함한 모든 gpt-5.x 앞에 온다', () => {
    expect(sorted([
      'gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-6-astra', 'gpt-5.5',
    ])).toEqual([
      'gpt-6-astra', 'gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.5',
    ]);
  });

  it('keeps GPT tier order Astra → Sol → Terra → Luna within one version', () => {
    expect(sorted([
      'gpt-6-luna', 'gpt-6-terra', 'gpt-6-sol', 'gpt-6-astra',
    ])).toEqual([
      'gpt-6-astra', 'gpt-6-sol', 'gpt-6-terra', 'gpt-6-luna',
    ]);
  });

  // claude 티어는 세대를 가로지르는 제품군이고 codex 티어는 한 세대 안의 변종이라,
  // 어느 쪽이 먼저인지가 계열마다 다르다.
  it('claude 는 티어가 버전보다 먼저고 codex 는 그 반대다', () => {
    expect(sorted(['sonnet 5', 'opus 4.8'])).toEqual(['opus 4.8', 'sonnet 5']);
    expect(sorted(['gpt-5.5', 'gpt-5.6-luna'])).toEqual(['gpt-5.6-luna', 'gpt-5.5']);
  });

  it('mini 는 같은 세대의 기본 모델보다 뒤에 온다', () => {
    expect(sorted(['gpt-5.4-mini', 'gpt-5.4'])).toEqual(['gpt-5.4', 'gpt-5.4-mini']);
    expect(sorted(['gpt-5.6-luna', 'gpt-5.6-mini', 'gpt-5.6-sol'])).toEqual(['gpt-5.6-sol', 'gpt-5.6-luna', 'gpt-5.6-mini']);
  });

  it('버전 없는 라벨은 같은 티어의 버전 있는 라벨보다 뒤에 온다', () => {
    expect(sorted(['sonnet', 'sonnet 5'])).toEqual(['sonnet 5', 'sonnet']);
  });

  it('Others는 계열과 무관하게 항상 마지막이다', () => {
    expect(sorted(['Others', 'gemma4:12b', 'opus 5'])).toEqual([
      'opus 5', 'gemma4:12b', 'Others',
    ]);
  });

  it('정렬 결과는 입력 순서와 무관하게 동일하다', () => {
    const labels = ['gpt-5.5', 'opus 5', 'haiku 4.5', 'gemma4:12b', 'fable 5'];
    expect(sorted(labels)).toEqual(sorted([...labels].reverse()));
  });
});

/**
 * 합성 데이터를 만드는 결정적 난수. 같은 시드는 언제나 같은 주를 만든다 — 미리보기도 시험도 매번 같은 화면이어야 한다.
 */

/** mulberry32: 32비트 시드에서 [0, 1) 난수 함수를 만든다. */
export const mulberry32 = (seed: number): (() => number) => {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
};

export interface Rng {
  /** [0, 1) */
  next(): number;
  /** [lo, hi] 정수 */
  int(lo: number, hi: number): number;
  /** [lo, hi) 실수 */
  between(lo: number, hi: number): number;
  chance(p: number): boolean;
  pick<T>(items: readonly T[]): T;
  /** 가중치에 비례해 하나를 고른다. */
  weighted<T>(items: readonly { value: T; weight: number }[]): T;
  /** 16진수 문자열 `length`자. */
  hex(length: number): string;
}

export const createRng = (seed: number): Rng => {
  const next = mulberry32(seed);
  const int = (lo: number, hi: number): number => lo + Math.floor(next() * (hi - lo + 1));
  return {
    next,
    int,
    between: (lo, hi) => lo + next() * (hi - lo),
    chance: (p) => next() < p,
    pick: (items) => items[Math.floor(next() * items.length)],
    weighted: (items) => {
      const total = items.reduce((s, i) => s + i.weight, 0);
      let r = next() * total;
      for (const item of items) {
        r -= item.weight;
        if (r < 0) return item.value;
      }
      return items[items.length - 1].value;
    },
    hex: (length) => Array.from({ length }, () => int(0, 15).toString(16)).join(''),
  };
};

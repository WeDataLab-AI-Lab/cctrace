import { describe, expect, it } from 'vitest';
import {
  BUTTON_FACTOR,
  DEFAULT_PX_PER_HOUR,
  MAX_PX_PER_HOUR,
  MIN_PX_PER_HOUR,
  WHEEL_FACTOR,
  canZoomIn,
  canZoomOut,
  clampZoom,
  wheelZoom,
  zoomIn,
  zoomOut,
} from './zoom';

describe('확대·축소(C3): 시간당 12~240px', () => {
  it('범위와 배율', () => {
    expect(MIN_PX_PER_HOUR).toBe(12);
    expect(MAX_PX_PER_HOUR).toBe(240);
    expect(BUTTON_FACTOR).toBe(1.25);
    expect(WHEEL_FACTOR).toBe(1.1);
    expect(DEFAULT_PX_PER_HOUR).toBeGreaterThanOrEqual(MIN_PX_PER_HOUR);
    expect(DEFAULT_PX_PER_HOUR).toBeLessThanOrEqual(MAX_PX_PER_HOUR);
  });

  it('버튼은 1.25배씩', () => {
    expect(zoomIn(40)).toBeCloseTo(50, 10);
    expect(zoomOut(50)).toBeCloseTo(40, 10);
    expect(zoomIn(zoomOut(33))).toBeCloseTo(33, 10);
  });

  it('끝에서 멈춘다', () => {
    expect(zoomIn(MAX_PX_PER_HOUR)).toBe(MAX_PX_PER_HOUR);
    expect(zoomIn(200)).toBe(240);
    expect(zoomOut(MIN_PX_PER_HOUR)).toBe(MIN_PX_PER_HOUR);
    expect(zoomOut(13)).toBe(12);
    expect(clampZoom(1)).toBe(12);
    expect(clampZoom(9999)).toBe(240);
  });

  it('휠 한 칸(deltaY ±100)은 정확히 1.1배: 위로 굴리면 확대, 아래로 굴리면 축소', () => {
    expect(wheelZoom(40, -100)).toBeCloseTo(44, 10);
    expect(wheelZoom(44, 100)).toBeCloseTo(40, 10);
  });

  it('작은 deltaY(트랙패드 핀치)는 그만큼만, 큰 deltaY는 세 칸에서 멈춘다', () => {
    expect(wheelZoom(40, -10)).toBeCloseTo(40 * 1.1 ** 0.1, 10);
    expect(wheelZoom(40, -100000)).toBeCloseTo(40 * 1.1 ** 3, 10);
    expect(wheelZoom(40, 0)).toBe(40);
  });

  it('휠도 범위를 넘지 않는다', () => {
    expect(wheelZoom(238, -300)).toBe(240);
    expect(wheelZoom(12.5, 300)).toBe(12);
  });

  it('더 확대·축소할 수 있는지', () => {
    expect(canZoomIn(30)).toBe(true);
    expect(canZoomIn(MAX_PX_PER_HOUR)).toBe(false);
    expect(canZoomOut(30)).toBe(true);
    expect(canZoomOut(MIN_PX_PER_HOUR)).toBe(false);
  });
});

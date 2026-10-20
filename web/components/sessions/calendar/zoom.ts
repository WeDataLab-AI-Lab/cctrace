/**
 * 확대·축소(C3). 시간당 픽셀(px/h)을 12~240 사이에서 바꾼다.
 * 버튼은 1.25배, Ctrl/Cmd+휠은 한 칸(deltaY ±100)에 1.1배다. 트랙패드 핀치처럼 deltaY가 작은 이벤트는 그 비율만큼만
 * 움직여 한 번에 튀지 않게 한다.
 */

const MIN_PX_PER_HOUR = 12;
const MAX_PX_PER_HOUR = 240;
/** 처음 배율("표준"). */
const DEFAULT_PX_PER_HOUR = 30;
const BUTTON_FACTOR = 1.25;
const WHEEL_FACTOR = 1.1;
/** 휠 한 칸의 deltaY. 이 값에서 정확히 `WHEEL_FACTOR`배가 된다. */
const WHEEL_NOTCH = 100;

const clampZoom = (px: number): number => Math.min(MAX_PX_PER_HOUR, Math.max(MIN_PX_PER_HOUR, px));

const zoomIn = (px: number): number => clampZoom(px * BUTTON_FACTOR);
const zoomOut = (px: number): number => clampZoom(px / BUTTON_FACTOR);

/** 휠 이벤트 하나에 대한 새 배율. deltaY가 음수(위로 굴림)면 확대다. */
const wheelZoom = (px: number, deltaY: number): number => {
  if (deltaY === 0) return px;
  // 한 이벤트가 3칸보다 크게 움직이지 않게 막는다(관성 스크롤).
  const notches = Math.min(3, Math.abs(deltaY) / WHEEL_NOTCH);
  const factor = WHEEL_FACTOR ** notches;
  return clampZoom(deltaY < 0 ? px * factor : px / factor);
};

const canZoomIn = (px: number): boolean => px < MAX_PX_PER_HOUR - 1e-6;
const canZoomOut = (px: number): boolean => px > MIN_PX_PER_HOUR + 1e-6;

export {
  MIN_PX_PER_HOUR,
  MAX_PX_PER_HOUR,
  DEFAULT_PX_PER_HOUR,
  BUTTON_FACTOR,
  WHEEL_FACTOR,
  clampZoom,
  zoomIn,
  zoomOut,
  wheelZoom,
  canZoomIn,
  canZoomOut,
};

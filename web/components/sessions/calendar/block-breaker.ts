/**
 * 블록 깨기(C19). 캘린더의 띠를 벽돌로 쓴다. 이 파일은 게임의 규칙만 가진다 — 그리기와 입력은 컴포넌트가 한다.
 *
 * 좌표는 오버레이(격자의 보이는 영역) 안의 픽셀이다. 위쪽이 0이다. 상태는 불변 값이라 시험하기 쉽고, 한 걸음(`step`)은
 * 시간만 받는다. 시간 조각을 잘게 나눠 공이 얇은 벽돌을 뚫고 지나가지 않게 한다.
 *
 * 벽돌은 세션의 띠다. 깨도 데이터는 바뀌지 않는다(오버레이 안의 놀이일 뿐이다).
 */

export interface GameBrick {
  id: string;
  x: number;
  y: number;
  w: number;
  h: number;
  /** 벽돌 색(CSS 값). 띠의 색과 같다. */
  token: string;
}

export interface GameBall {
  x: number;
  y: number;
  vx: number;
  vy: number;
  r: number;
}

export interface GamePaddle {
  /** 가운데 x. */
  x: number;
  y: number;
  w: number;
  h: number;
}

export type GamePhase = 'ready' | 'playing' | 'won' | 'lost';

export interface GameState {
  width: number;
  height: number;
  /** 아직 깨지 않은 벽돌. */
  bricks: GameBrick[];
  /** 처음 벽돌 수. */
  total: number;
  paddle: GamePaddle;
  ball: GameBall;
  /** 공의 속도(px/s). 벽돌을 깰수록 조금씩 빨라진다. */
  speed: number;
  /** 깬 벽돌 수. */
  score: number;
  lives: number;
  phase: GamePhase;
}

const START_LIVES = 3;
const BALL_RADIUS = 5;
const BASE_SPEED = 340;
const MAX_SPEED = 560;
const SPEED_UP_PER_BRICK = 3;
const PADDLE_HEIGHT = 9;
const PADDLE_BOTTOM_MARGIN = 22;
/** 벽돌이 너무 작으면 공이 뚫고 지나가거나 맞히기 어려우므로 이만큼으로 키운다. */
const MIN_BRICK_W = 10;
const MIN_BRICK_H = 6;
/** 시간 조각의 최댓값(ms). 한 번에 이보다 길게 움직이지 않는다. */
const SUBSTEP_MS = 4;
/** 한 프레임이 이보다 길면 잘라 낸다(탭이 백그라운드에 있다 돌아왔을 때 공이 튀지 않게). */
const MAX_FRAME_MS = 50;
/** 패들 끝에 맞았을 때 수직에서 꺾이는 최대 각도(라디안). */
const MAX_BOUNCE_ANGLE = (62 * Math.PI) / 180;

const clamp = (n: number, lo: number, hi: number): number => Math.min(hi, Math.max(lo, n));

/** 너무 작은 벽돌은 가운데를 지키며 최소 크기로 키운다. */
const growBrick = (b: GameBrick): GameBrick => {
  const w = Math.max(b.w, MIN_BRICK_W);
  const h = Math.max(b.h, MIN_BRICK_H);
  return { ...b, x: b.x - (w - b.w) / 2, y: b.y - (h - b.h) / 2, w, h };
};

const createGame = ({ width, height, bricks }: { width: number; height: number; bricks: readonly GameBrick[] }): GameState => {
  const paddleW = Math.min(width, Math.max(72, Math.round(width * 0.14)));
  const paddle: GamePaddle = { x: width / 2, y: Math.max(BALL_RADIUS * 2 + 2, height - PADDLE_BOTTOM_MARGIN), w: paddleW, h: PADDLE_HEIGHT };
  // 화면 아래쪽의 활동 띠가 패들 출발점과 겹치지 않도록 필요한 띠만 올린다.
  const safeBottom = Math.max(0, paddle.y - BALL_RADIUS * 4);
  const grown = bricks.map((brick) => {
    const grownBrick = growBrick(brick);
    return { ...grownBrick, y: Math.max(0, Math.min(grownBrick.y, safeBottom - grownBrick.h)) };
  });
  return {
    width,
    height,
    bricks: grown,
    total: grown.length,
    paddle,
    ball: { x: paddle.x, y: paddle.y - BALL_RADIUS - 1, vx: 0, vy: 0, r: BALL_RADIUS },
    speed: BASE_SPEED,
    score: 0,
    lives: START_LIVES,
    phase: grown.length === 0 ? 'won' : 'ready',
  };
};

/** 패들을 옮긴다. 공이 패들 위에 있으면(`ready`) 공도 따라간다. */
const movePaddle = (state: GameState, centerX: number): GameState => {
  const half = state.paddle.w / 2;
  const x = clamp(centerX, half, state.width - half);
  const paddle = { ...state.paddle, x };
  const ball = state.phase === 'ready' ? { ...state.ball, x } : state.ball;
  return { ...state, paddle, ball };
};

/** 공을 쏜다. 패들 위치가 가운데에서 벗어난 만큼 그쪽으로 비스듬히 나간다. */
const launch = (state: GameState): GameState => {
  if (state.phase !== 'ready') return state;
  const lean = clamp((state.paddle.x - state.width / 2) / (state.width / 2), -1, 1);
  const angle = (0.25 + 0.35 * lean) * (Math.PI / 2);
  return { ...state, phase: 'playing', ball: { ...state.ball, vx: Math.sin(angle) * state.speed, vy: -Math.cos(angle) * state.speed } };
};

interface Hit {
  nx: number;
  ny: number;
  depth: number;
}

/** 원과 사각형이 겹치면 사각형 바깥쪽 법선과 파고든 깊이. */
const circleRect = (ball: GameBall, rect: { x: number; y: number; w: number; h: number }): Hit | null => {
  const cx = clamp(ball.x, rect.x, rect.x + rect.w);
  const cy = clamp(ball.y, rect.y, rect.y + rect.h);
  const dx = ball.x - cx;
  const dy = ball.y - cy;
  const d2 = dx * dx + dy * dy;
  if (d2 > ball.r * ball.r) return null;
  if (d2 > 1e-9) {
    const d = Math.sqrt(d2);
    return { nx: dx / d, ny: dy / d, depth: ball.r - d };
  }
  // 공의 중심이 사각형 안에 있다: 가장 가까운 변으로 밀어낸다.
  const left = ball.x - rect.x;
  const right = rect.x + rect.w - ball.x;
  const top = ball.y - rect.y;
  const bottom = rect.y + rect.h - ball.y;
  const m = Math.min(left, right, top, bottom);
  if (m === left) return { nx: -1, ny: 0, depth: left + ball.r };
  if (m === right) return { nx: 1, ny: 0, depth: right + ball.r };
  if (m === top) return { nx: 0, ny: -1, depth: top + ball.r };
  return { nx: 0, ny: 1, depth: bottom + ball.r };
};

/** 속도를 법선에 대해 되튕긴다(법선 쪽으로 들어오고 있을 때만). */
const reflect = (ball: GameBall, hit: Hit): GameBall => {
  const dot = ball.vx * hit.nx + ball.vy * hit.ny;
  const moved = { ...ball, x: ball.x + hit.nx * hit.depth, y: ball.y + hit.ny * hit.depth };
  if (dot >= 0) return moved;
  return { ...moved, vx: ball.vx - 2 * dot * hit.nx, vy: ball.vy - 2 * dot * hit.ny };
};

const withSpeed = (ball: GameBall, speed: number): GameBall => {
  const current = Math.hypot(ball.vx, ball.vy);
  if (current === 0) return ball;
  const k = speed / current;
  return { ...ball, vx: ball.vx * k, vy: ball.vy * k };
};

const substep = (state: GameState, dt: number): GameState => {
  let ball: GameBall = { ...state.ball, x: state.ball.x + state.ball.vx * dt, y: state.ball.y + state.ball.vy * dt };
  let { bricks, score, speed } = state;

  // 벽(왼쪽, 오른쪽, 위).
  if (ball.x < ball.r) ball = { ...ball, x: ball.r, vx: Math.abs(ball.vx) };
  else if (ball.x > state.width - ball.r) ball = { ...ball, x: state.width - ball.r, vx: -Math.abs(ball.vx) };
  if (ball.y < ball.r) ball = { ...ball, y: ball.r, vy: Math.abs(ball.vy) };

  // 패들: 내려오는 공만 받는다. 맞은 자리에 따라 각도가 달라진다.
  const p = state.paddle;
  if (ball.vy > 0) {
    const hit = circleRect(ball, { x: p.x - p.w / 2, y: p.y, w: p.w, h: p.h });
    if (hit) {
      const t = clamp((ball.x - p.x) / (p.w / 2), -1, 1);
      const angle = t * MAX_BOUNCE_ANGLE;
      ball = { ...ball, y: p.y - ball.r - 0.01, vx: Math.sin(angle) * speed, vy: -Math.cos(angle) * speed };
    }
  }

  // 벽돌: 한 걸음에 하나만 깬다(가장 깊이 파고든 것).
  let hitIndex = -1;
  let deepest: Hit | null = null;
  for (let i = 0; i < bricks.length; i++) {
    const hit = circleRect(ball, bricks[i]);
    if (hit && (deepest === null || hit.depth > deepest.depth)) {
      hitIndex = i;
      deepest = hit;
    }
  }
  if (deepest !== null) {
    ball = reflect(ball, deepest);
    bricks = bricks.filter((_, i) => i !== hitIndex);
    score += 1;
    speed = Math.min(MAX_SPEED, speed + SPEED_UP_PER_BRICK);
    ball = withSpeed(ball, speed);
  }

  if (bricks.length === 0) return { ...state, ball, bricks, score, speed, phase: 'won' };

  // 바닥으로 빠졌다: 목숨 하나를 잃고 패들 위로 돌아온다.
  if (ball.y - ball.r > state.height) {
    const lives = state.lives - 1;
    const reset: GameBall = { x: p.x, y: p.y - state.ball.r - 1, vx: 0, vy: 0, r: state.ball.r };
    return { ...state, ball: reset, bricks, score, speed, lives, phase: lives <= 0 ? 'lost' : 'ready' };
  }
  return { ...state, ball, bricks, score, speed };
};

/** 시간을 `dtMs`만큼 흘린다. `playing`이 아니면 그대로다. */
const step = (state: GameState, dtMs: number): GameState => {
  if (state.phase !== 'playing') return state;
  let remaining = clamp(dtMs, 0, MAX_FRAME_MS);
  let next = state;
  while (remaining > 0 && next.phase === 'playing') {
    const slice = Math.min(SUBSTEP_MS, remaining);
    next = substep(next, slice / 1000);
    remaining -= slice;
  }
  return next;
};

export {
  START_LIVES,
  BALL_RADIUS,
  BASE_SPEED,
  MAX_SPEED,
  MIN_BRICK_W,
  MIN_BRICK_H,
  SUBSTEP_MS,
  MAX_FRAME_MS,
  createGame,
  movePaddle,
  launch,
  step,
  circleRect,
};

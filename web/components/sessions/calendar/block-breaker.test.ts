import { describe, expect, it } from 'vitest';
import {
  BASE_SPEED,
  MAX_FRAME_MS,
  MAX_SPEED,
  MIN_BRICK_H,
  MIN_BRICK_W,
  START_LIVES,
  circleRect,
  createGame,
  launch,
  movePaddle,
  step,
  type GameBrick,
  type GameState,
} from './block-breaker';

const brick = (id: string, x: number, y: number, w = 40, h = 12): GameBrick => ({ id, x, y, w, h, token: 'var(--cat-1)' });
const W = 400;
const H = 300;

/** 공을 원하는 자리·속도로 놓은 진행 중 상태. */
const playing = (s: GameState, ball: Partial<GameState['ball']>): GameState => ({ ...s, phase: 'playing', ball: { ...s.ball, ...ball } });

const run = (s: GameState, ms: number): GameState => {
  let next = s;
  for (let t = 0; t < ms; t += 16) next = step(next, 16);
  return next;
};

describe('시작', () => {
  it('공은 패들 위, 목숨 셋, 준비 상태다', () => {
    const g = createGame({ width: W, height: H, bricks: [brick('a', 10, 10)] });
    expect(g.phase).toBe('ready');
    expect(g.lives).toBe(START_LIVES);
    expect(g.score).toBe(0);
    expect(g.speed).toBe(BASE_SPEED);
    expect(g.ball.x).toBe(g.paddle.x);
    expect(g.ball.y).toBeLessThan(g.paddle.y);
    expect(g.total).toBe(1);
  });

  it('패들에 걸친 캘린더 띠는 출발 공 위로 옮겨 목숨을 곧바로 잃지 않는다', () => {
    const g = createGame({ width: W, height: H, bricks: [brick('bottom', 180, H - 25, 40, 25)] });
    expect(g.bricks[0].y + g.bricks[0].h).toBeLessThan(g.ball.y - g.ball.r);
    expect(step(launch(g), 16).lives).toBe(START_LIVES);
  });

  it('벽돌이 없으면 곧바로 끝(깰 것이 없다)', () => {
    expect(createGame({ width: W, height: H, bricks: [] }).phase).toBe('won');
  });

  it('너무 작은 벽돌은 가운데를 지키며 최소 크기로 키운다', () => {
    const g = createGame({ width: W, height: H, bricks: [brick('t', 100, 50, 2, 1)] });
    const b = g.bricks[0];
    expect(b.w).toBe(MIN_BRICK_W);
    expect(b.h).toBe(MIN_BRICK_H);
    expect(b.x + b.w / 2).toBeCloseTo(101, 10);
    expect(b.y + b.h / 2).toBeCloseTo(50.5, 10);
  });

  it('쏘면 위로 날아가고, 준비 상태가 아닐 때 쏘는 것은 무시한다', () => {
    const g = createGame({ width: W, height: H, bricks: [brick('a', 10, 10)] });
    const l = launch(g);
    expect(l.phase).toBe('playing');
    expect(l.ball.vy).toBeLessThan(0);
    expect(Math.hypot(l.ball.vx, l.ball.vy)).toBeCloseTo(BASE_SPEED, 6);
    expect(launch(l)).toBe(l);
  });
});

describe('패들', () => {
  it('화면 안에서만 움직이고, 준비 상태의 공은 따라간다', () => {
    const g = createGame({ width: W, height: H, bricks: [brick('a', 10, 10)] });
    const left = movePaddle(g, -50);
    expect(left.paddle.x).toBe(g.paddle.w / 2);
    expect(left.ball.x).toBe(left.paddle.x);
    const right = movePaddle(g, 9999);
    expect(right.paddle.x).toBe(W - g.paddle.w / 2);
  });

  it('진행 중의 공은 패들을 따라가지 않는다', () => {
    const g = playing(createGame({ width: W, height: H, bricks: [brick('a', 10, 10)] }), { x: 50, y: 100, vx: 0, vy: 100 });
    expect(movePaddle(g, 300).ball.x).toBe(50);
  });

  it('내려오는 공은 패들에 맞고 위로 튄다: 오른쪽 끝에 맞으면 오른쪽으로 꺾인다', () => {
    let g = createGame({ width: W, height: H, bricks: [brick('a', 10, 10)] });
    g = movePaddle(g, 200);
    const hitX = g.paddle.x + g.paddle.w / 2 - 2;
    g = playing(g, { x: hitX, y: g.paddle.y - 4, vx: 0, vy: 200 });
    const after = step(g, 16);
    expect(after.ball.vy).toBeLessThan(0);
    expect(after.ball.vx).toBeGreaterThan(0);
  });

  it('가운데에 맞으면 곧장 위로 간다', () => {
    let g = createGame({ width: W, height: H, bricks: [brick('a', 10, 10)] });
    g = playing(movePaddle(g, 200), { x: 200, y: g.paddle.y - 4, vx: 0, vy: 200 });
    const after = step(g, 16);
    expect(Math.abs(after.ball.vx)).toBeLessThan(1e-6);
    expect(after.ball.vy).toBeLessThan(0);
  });
});

describe('벽', () => {
  it('왼쪽·오른쪽·위쪽 벽에서 되튄다', () => {
    const g = createGame({ width: W, height: H, bricks: [brick('far', 380, 280)] });
    const left = step(playing(g, { x: 6, y: 150, vx: -300, vy: 0 }), 16);
    expect(left.ball.vx).toBeGreaterThan(0);
    const right = step(playing(g, { x: W - 6, y: 150, vx: 300, vy: 0 }), 16);
    expect(right.ball.vx).toBeLessThan(0);
    const top = step(playing(g, { x: 150, y: 6, vx: 0, vy: -300 }), 16);
    expect(top.ball.vy).toBeGreaterThan(0);
  });
});

describe('벽돌', () => {
  it('맞은 벽돌은 사라지고 점수가 오르고 공이 빨라진다', () => {
    const g = createGame({ width: W, height: H, bricks: [brick('a', 180, 100), brick('b', 20, 20)] });
    const hit = step(playing(g, { x: 200, y: 118, vx: 0, vy: -300 }), 16);
    expect(hit.bricks.map((b) => b.id)).toEqual(['b']);
    expect(hit.score).toBe(1);
    expect(hit.speed).toBeGreaterThan(BASE_SPEED);
    expect(hit.ball.vy).toBeGreaterThan(0); // 아래에서 맞았으니 아래로 되튄다
  });

  it('위에서 맞으면 위로, 옆에서 맞으면 옆으로 되튄다', () => {
    const g = createGame({ width: W, height: H, bricks: [brick('a', 180, 100), brick('b', 20, 20)] });
    const top = step(playing(g, { x: 200, y: 96, vx: 0, vy: 300 }), 16);
    expect(top.ball.vy).toBeLessThan(0);
    const side = step(playing(g, { x: 176, y: 106, vx: 300, vy: 0 }), 16);
    expect(side.ball.vx).toBeLessThan(0);
  });

  it('공이 벽돌에서 멀면 아무것도 깨지지 않는다', () => {
    const g = createGame({ width: W, height: H, bricks: [brick('a', 180, 100), brick('b', 20, 20)] });
    const free = step(playing(g, { x: 50, y: 200, vx: 10, vy: 10 }), 16);
    expect(free.bricks).toHaveLength(2);
    expect(free.score).toBe(0);
  });

  it('마지막 벽돌을 깨면 끝난다', () => {
    const g = createGame({ width: W, height: H, bricks: [brick('only', 180, 100)] });
    const won = step(playing(g, { x: 200, y: 118, vx: 0, vy: -300 }), 16);
    expect(won.phase).toBe('won');
    expect(won.score).toBe(1);
  });

  it('속도에는 상한이 있다', () => {
    const bricks = Array.from({ length: 200 }, (_, i) => brick(`b${i}`, (i % 20) * 19, Math.floor(i / 20) * 14 + 10, 18, 12));
    let g = createGame({ width: W, height: H, bricks });
    g = { ...g, speed: MAX_SPEED - 1 };
    const hit = step(playing(g, { x: 9, y: 22, vx: 0, vy: -100 }), 16);
    expect(hit.speed).toBeLessThanOrEqual(MAX_SPEED);
  });

  it('얇은 벽돌을 빠른 공이 뚫고 지나가지 않는다(시간을 잘게 나눈다)', () => {
    const thin = brick('thin', 0, 150, W, MIN_BRICK_H);
    const g = createGame({ width: W, height: H, bricks: [thin, brick('x', 5, 5)] });
    const fast = { ...playing(g, { x: 200, y: 200, vx: 0, vy: -3000 }), speed: 3000 };
    const after = step(fast, MAX_FRAME_MS);
    expect(after.bricks.map((b) => b.id)).toEqual(['x']);
    expect(after.score).toBe(1);
  });
});

describe('목숨', () => {
  it('바닥으로 빠지면 목숨 하나를 잃고 패들 위에서 다시 준비한다', () => {
    const g = createGame({ width: W, height: H, bricks: [brick('a', 10, 10)] });
    const fell = step(playing(g, { x: 20, y: H + 20, vx: 0, vy: 200 }), 16);
    expect(fell.lives).toBe(START_LIVES - 1);
    expect(fell.phase).toBe('ready');
    expect(fell.ball.vx).toBe(0);
    expect(fell.ball.vy).toBe(0);
    expect(fell.ball.x).toBe(fell.paddle.x);
  });

  it('마지막 목숨을 잃으면 진다', () => {
    const g = { ...createGame({ width: W, height: H, bricks: [brick('a', 10, 10)] }), lives: 1 };
    const lost = step(playing(g, { x: 20, y: H + 20, vx: 0, vy: 200 }), 16);
    expect(lost.lives).toBe(0);
    expect(lost.phase).toBe('lost');
  });

  it('진행 중이 아니면 시간이 흘러도 그대로다', () => {
    const g = createGame({ width: W, height: H, bricks: [brick('a', 10, 10)] });
    expect(step(g, 16)).toBe(g);
    const won = { ...g, phase: 'won' as const };
    expect(step(won, 16)).toBe(won);
  });
});

describe('충돌 계산', () => {
  it('원이 사각형에서 떨어져 있으면 충돌이 없고, 닿으면 바깥쪽 법선을 준다', () => {
    const rect = { x: 100, y: 100, w: 40, h: 12 };
    expect(circleRect({ x: 50, y: 50, vx: 0, vy: 0, r: 5 }, rect)).toBeNull();
    const hit = circleRect({ x: 120, y: 96, vx: 0, vy: 0, r: 5 }, rect);
    expect(hit).toMatchObject({ nx: 0, ny: -1 });
    expect(hit?.depth).toBeCloseTo(1, 10);
  });

  it('중심이 사각형 안에 있으면 가장 가까운 변으로 민다', () => {
    const rect = { x: 100, y: 100, w: 40, h: 12 };
    expect(circleRect({ x: 101, y: 106, vx: 0, vy: 0, r: 5 }, rect)).toMatchObject({ nx: -1, ny: 0 });
  });

  it('긴 프레임은 잘라 낸다: 한 번에 움직이는 거리에 상한이 있다', () => {
    const g = createGame({ width: 10_000, height: 10_000, bricks: [brick('a', 5, 5)] });
    const far = playing(g, { x: 5000, y: 5000, vx: 100, vy: 0 });
    const after = run(far, 10_000);
    // 한 프레임(16ms)마다 최대 MAX_FRAME_MS가 아니라 16ms씩 흘렀다: 10초 = 1000px.
    expect(after.ball.x).toBeGreaterThan(5900);
    const single = step(far, 5000);
    expect(single.ball.x - 5000).toBeLessThanOrEqual((100 * MAX_FRAME_MS) / 1000 + 1e-6);
  });
});

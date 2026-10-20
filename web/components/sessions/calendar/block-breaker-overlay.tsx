'use client';

import { useEffect, useEffectEvent, useRef, useState } from 'react';
import { X } from 'lucide-react';
import { cn } from '@/lib/utils';
import { HEADER_HEIGHT } from './calendar-grid';
import { Abs } from './css-box';
import { createGame, launch, movePaddle, step, type GameBrick, type GamePhase, type GameState } from './block-breaker';

/**
 * 블록 깨기(C19). 캘린더의 띠를 벽돌로 쓰는 장난이다. 보이는 영역 위에 덮이는 오버레이이고, 닫으면 캘린더는 그대로다
 * (벽돌을 깨도 데이터는 바뀌지 않는다). 규칙은 `block-breaker.ts`가 갖고, 이 파일은 그리기와 입력만 한다.
 *
 * 조작: 마우스나 ← → 로 패들, 클릭이나 Space로 발사, P로 일시정지, Esc로 닫기.
 */

export interface GameStart {
  width: number;
  height: number;
  bricks: GameBrick[];
}

interface Hud {
  score: number;
  total: number;
  lives: number;
  phase: GamePhase;
}

const KEY_STEP = 28;

const BlockBreakerOverlay = ({ start, onClose }: { start: GameStart; onClose: () => void }) => {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  const [round, setRound] = useState(0);
  const [paused, setPaused] = useState(false);
  const [hud, setHud] = useState<Hud>({ score: 0, total: start.bricks.length, lives: 3, phase: 'ready' });
  const pausedRef = useRef(false);

  /** A retry removes the result buttons, so return keyboard focus to the game board for the new round. */
  useEffect(() => {
    canvasRef.current?.focus();
  }, [round]);

  const close = useEffectEvent(onClose);
  const togglePause = useEffectEvent(() => {
    pausedRef.current = !pausedRef.current;
    setPaused(pausedRef.current);
  });

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    // 그리기 맥락을 얻지 못해도(제한된 환경) 키보드 조작, 특히 Esc로 닫기는 되어야 한다. 그릴 곳이 없을 뿐이다.
    const ctx = canvas.getContext('2d');
    const dpr = Math.min(2, window.devicePixelRatio || 1);
    canvas.width = Math.round(start.width * dpr);
    canvas.height = Math.round(start.height * dpr);
    ctx?.setTransform(dpr, 0, 0, dpr, 0, 0);
    const ink = getComputedStyle(canvas).color;

    let state: GameState = createGame(start);
    let published = '';
    let last = performance.now();
    let raf = 0;

    const publish = () => {
      const key = `${state.score}|${state.lives}|${state.phase}`;
      if (key === published) return;
      published = key;
      setHud({ score: state.score, total: state.total, lives: state.lives, phase: state.phase });
    };

    const draw = () => {
      if (!ctx) return;
      ctx.clearRect(0, 0, start.width, start.height);
      for (const brick of state.bricks) {
        ctx.globalAlpha = 0.28;
        ctx.fillStyle = brick.token;
        ctx.fillRect(brick.x, brick.y, brick.w, brick.h);
        ctx.globalAlpha = 1;
        ctx.strokeStyle = brick.token;
        ctx.lineWidth = 1.5;
        ctx.strokeRect(brick.x + 0.75, brick.y + 0.75, Math.max(0, brick.w - 1.5), Math.max(0, brick.h - 1.5));
      }
      ctx.fillStyle = ink;
      const p = state.paddle;
      ctx.fillRect(p.x - p.w / 2, p.y, p.w, p.h);
      ctx.beginPath();
      ctx.arc(state.ball.x, state.ball.y, state.ball.r, 0, Math.PI * 2);
      ctx.fill();
    };

    const frame = (now: number) => {
      const dt = now - last;
      last = now;
      if (!pausedRef.current) state = step(state, dt);
      draw();
      publish();
      raf = requestAnimationFrame(frame);
    };
    raf = requestAnimationFrame(frame);

    const onMove = (event: PointerEvent) => {
      const rect = canvas.getBoundingClientRect();
      state = movePaddle(state, event.clientX - rect.left);
    };
    const onDown = () => {
      canvas.focus();
      state = launch(state);
    };
    const onKey = (event: KeyboardEvent) => {
      const dialog = dialogRef.current;
      if (!dialog?.contains(event.target as Node)) return;
      if (event.key === 'Tab') {
        const controls = Array.from(dialog.querySelectorAll<HTMLElement>('button:not(:disabled), canvas[tabindex="0"]'));
        const current = controls.indexOf(document.activeElement as HTMLElement);
        if (controls.length > 0 && (event.shiftKey ? current <= 0 : current === controls.length - 1)) {
          event.preventDefault();
          controls[event.shiftKey ? controls.length - 1 : 0].focus();
        }
        return;
      }
      if (event.key === 'Escape') {
        event.preventDefault();
        close();
        return;
      }
      if (event.target !== canvas || event.ctrlKey || event.metaKey || event.altKey) return;
      if (event.key === 'ArrowLeft') {
        event.preventDefault();
        state = movePaddle(state, state.paddle.x - KEY_STEP);
      } else if (event.key === 'ArrowRight') {
        event.preventDefault();
        state = movePaddle(state, state.paddle.x + KEY_STEP);
      } else if (event.key === ' ') {
        event.preventDefault();
        state = launch(state);
      } else if (event.key === 'p' || event.key === 'P') {
        event.preventDefault();
        togglePause();
      }
    };
    canvas.addEventListener('pointermove', onMove);
    canvas.addEventListener('pointerdown', onDown);
    const dialog = dialogRef.current;
    dialog?.addEventListener('keydown', onKey);
    return () => {
      cancelAnimationFrame(raf);
      canvas.removeEventListener('pointermove', onMove);
      canvas.removeEventListener('pointerdown', onDown);
      dialog?.removeEventListener('keydown', onKey);
    };
  }, [start, round]);

  const ended = hud.phase === 'won' || hud.phase === 'lost';
  return (
    <div ref={dialogRef} role="dialog" aria-modal="true" aria-label="블록 깨기" className="absolute inset-0 z-[60] bg-surface/85 backdrop-blur-[1px]">
      {/* 안내줄은 격자의 날짜 머리 자리에 놓는다. 벽돌은 그 아래의 띠들이다. */}
      <Abs
        vars={{ '--h': `${HEADER_HEIGHT}px` }}
        className="absolute inset-x-0 top-0 z-[1] flex h-[var(--h)] items-center gap-3 border-b border-border bg-surface px-3 text-[12px]"
      >
        <strong className="font-semibold text-ink">블록 깨기</strong>
        <span className="tabular-nums text-ink-2" aria-live="polite">
          깬 띠 {hud.score} / {hud.total}
        </span>
        <span className="tabular-nums text-ink-2" aria-label={`목숨 ${hud.lives}개`}>
          목숨 {'●'.repeat(Math.max(0, hud.lives))}
          <span className="text-ink-4">{'●'.repeat(Math.max(0, 3 - hud.lives))}</span>
        </span>
        <span className="hidden min-w-0 truncate text-[11px] text-ink-3 lg:inline">마우스·← → 패들 · 클릭·Space 발사 · P 일시정지 · Esc 닫기</span>
        <button
          type="button"
          onClick={onClose}
          aria-label="블록 깨기 닫기"
          className="ml-auto inline-flex size-6 shrink-0 items-center justify-center rounded text-ink-3 hover:bg-surface-sunk hover:text-ink"
        >
          <X size={14} strokeWidth={2} aria-hidden="true" />
        </button>
      </Abs>
      {/* 판: 벽돌 좌표와 같은 좌표계다(날짜 머리 아래 전체). */}
      <Abs vars={{ '--top': `${HEADER_HEIGHT}px` }} className="absolute inset-x-0 top-[var(--top)] bottom-0">
        <canvas
          ref={canvasRef}
          tabIndex={0}
          aria-label="블록 깨기 화면"
          className={cn('absolute inset-0 h-full w-full cursor-none touch-none text-ink outline-none', paused && 'opacity-60')}
        />
        {(hud.phase === 'ready' || paused || ended) && (
          <div className="pointer-events-none absolute inset-x-0 top-1/2 flex -translate-y-1/2 flex-col items-center gap-1 text-center">
            <p className="rounded bg-surface/90 px-3 py-1.5 text-[13px] font-semibold text-ink shadow-[var(--sh-sm)]">
              {hud.phase === 'won'
                ? `클리어! 띠 ${hud.total}개를 모두 깼습니다`
                : hud.phase === 'lost'
                  ? '게임 오버'
                  : paused
                    ? '일시정지'
                    : '클릭하거나 Space를 눌러 공을 쏘세요'}
            </p>
          </div>
        )}
        {ended && (
          <div className="absolute inset-x-0 bottom-8 flex justify-center gap-2">
            <button
              type="button"
              onClick={() => {
                setHud({ score: 0, total: start.bricks.length, lives: 3, phase: 'ready' });
                setRound((r) => r + 1);
              }}
              className="rounded border border-border bg-surface px-3 py-1.5 text-[12px] font-medium text-ink hover:bg-surface-sunk"
            >
              다시 하기
            </button>
            <button type="button" onClick={onClose} className="rounded border border-border bg-surface px-3 py-1.5 text-[12px] font-medium text-ink hover:bg-surface-sunk">
              캘린더로 돌아가기
            </button>
          </div>
        )}
      </Abs>
    </div>
  );
};

export { BlockBreakerOverlay };

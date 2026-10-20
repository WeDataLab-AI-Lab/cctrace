'use client';

import type { ComponentPropsWithoutRef, CSSProperties } from 'react';

/**
 * 위치와 크기를 CSS 변수로만 받는 상자. 캘린더의 띠·눈금·툴팁은 좌표가 실행 중에 정해지는데 Tailwind 클래스는 정해진 값만
 * 표현하므로, 코드베이스의 관례(`tool-rate-bar.tsx`, `timeline-navigator.tsx`)대로 **CSS 변수를 style 속성으로만** 넘기고
 * 값은 클래스의 `top-[var(--y)]` 같은 임의 값으로 쓴다. ESLint FRONT-RULE은 JSX `style`을 막지만 "동적 값은 CSS 변수와 사유가 적힌
 * 줄 단위 예외로 처리한다"고 설정이 직접 적어 두었다. 이 파일의 두 컴포넌트가 캘린더에서 `style`을 쓰는 **유일한** 곳이고,
 * 색·모양은 모두 클래스(토큰)로 쓴다.
 */

export type CssVars = Record<`--${string}`, string>;

type BoxProps = { vars: CssVars } & ComponentPropsWithoutRef<'div'>;
type ButtonBoxProps = { vars: CssVars } & ComponentPropsWithoutRef<'button'>;

const Abs = ({ vars, ...rest }: BoxProps) => (
  // CSS 변수로만 위치·크기를 넘긴다(위 설명). 이 줄이 이 컴포넌트의 유일한 예외다.
  <div {...rest} style={vars as CSSProperties} /> // eslint-disable-line no-restricted-syntax -- 좌표는 실행 중에 정해져 CSS 변수로만 넘긴다
);

const AbsButton = ({ vars, ...rest }: ButtonBoxProps) => (
  // 위와 같다: 띠는 누를 수 있어야 해서 버튼이다.
  <button {...rest} style={vars as CSSProperties} /> // eslint-disable-line no-restricted-syntax -- 좌표는 실행 중에 정해져 CSS 변수로만 넘긴다
);

export { Abs, AbsButton };

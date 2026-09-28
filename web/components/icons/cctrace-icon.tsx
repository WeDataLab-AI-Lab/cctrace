import { cn } from '@/lib/utils';

interface CctraceIconProps {
  size?: number;
  className?: string;
  /** Paint the mark in something other than ink. Still one flat colour. */
  accent?: string;
}

/**
 * cctrace mark — an eye, drawn in one flat colour.
 *
 * Achromatic on purpose, at every size. The mark used to carry a Claude-orange
 * to Codex-violet ramp so that "the agents are equal" was said in the logo
 * itself, but a logo is not where that belongs: it is the one element that
 * appears beside every screen, and a coloured mark competes with the charts,
 * which is where the agent palette actually has work to do. Ink says nothing
 * about any one agent, which is the more honest way to say they are equal.
 *
 * The drawing is sized for where it is used. In the sidebar it renders at 36px
 * and the viewBox scales by 36/512, so a unit of stroke is 0.07px. Two things
 * follow and are deliberate:
 *
 *  - The almond is 300x240, not the 360x200 it began as. The wide version left
 *    most of its box empty and read small beside the wordmark.
 *  - Its curves reach the left and right tips at a wide angle. Meeting them
 *    head-on made cusps, and a cusp under a thin stroke turns to mush.
 *
 * Opacity is the caller's business: the sidebar sets it to 30% so the mark is a
 * ground the name sits on rather than a second thing competing with it.
 */
const CctraceIcon = ({ size = 24, className, accent }: CctraceIconProps) => {
  const paint = accent ?? 'var(--ink)';
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 512 512"
      width={size}
      height={size}
      className={cn(className)}
    >
      <path
        fill="none"
        stroke={paint}
        strokeWidth="34"
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M 106,256 C 150,166 200,136 256,136 C 312,136 362,166 406,256 C 362,346 312,376 256,376 C 200,376 150,346 106,256 Z"
      />
      <circle fill="none" stroke={paint} strokeWidth="34" cx="256" cy="256" r="80" />
      <circle fill={paint} cx="256" cy="256" r="30" />
    </svg>
  );
};

export { CctraceIcon };

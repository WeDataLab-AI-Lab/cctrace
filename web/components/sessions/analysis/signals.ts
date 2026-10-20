/**
 * 관측 신호 다섯 개를 계획 §5.2의 문장으로 바꾼다.
 *
 * 신호는 평가가 아니라 관측 문장이다. 이 파일은 신호를 합쳐 "닫아도 됨" 같은 결론을 만들지 않는다.
 * 키가 없는 신호(그 에이전트가 낼 수 없는 신호, 또는 비소유 관리자 화면)는 줄로 만들지 않는다.
 * `undeterminable`은 0이 아니다: 회색 줄이 되고, 이유는 `reason`에 담긴다.
 */

import type {
  SessionRowSignals,
  SessionSignals,
  SignalUndeterminableReason,
  TurnSignalValue,
  WaitingKind,
} from '@/lib/session-analysis-types';
import { formatClockNear } from './format';
import { UNKNOWN_TEXT, waitingKindLabel } from './labels';

type SignalKey = 'turn' | 'question' | 'background' | 'uncommitted' | 'todo';

/** 계획 §5.2 표의 순서. */
const SIGNAL_ORDER: readonly SignalKey[] = ['turn', 'question', 'background', 'uncommitted', 'todo'];

/** 목록 행의 짧은 꼴에서 값 앞에 붙는 말. */
const SIGNAL_LABEL: Record<SignalKey, string> = {
  turn: '턴',
  question: '질문',
  background: '백그라운드',
  uncommitted: '미커밋',
  todo: '할 일',
};

/** 문장의 주어. "<주어> 3", "<주어> 없음", "<주어> 판단 불가"로 쓴다. */
const SIGNAL_SUBJECT: Record<Exclude<SignalKey, 'turn'>, string> = {
  question: '답하지 않은 질문',
  background: '백그라운드 작업',
  uncommitted: '편집 후 미커밋 파일',
  todo: '미완료 할 일',
};

/**
 * quiet      관측했고 남은 것이 없다(또는 이미 끝났다)
 * attention  관측했고 남은 것이 있다
 * live       지금 진행 중이다
 * danger     오류로 끝났다
 * unknown    판단 불가(회색, 점선). 0이 아니다
 */
type SignalTone = 'quiet' | 'attention' | 'live' | 'danger' | 'unknown';

interface SignalLine {
  key: SignalKey;
  /** 줄 머리말. "턴", "질문", ... */
  label: string;
  /** 계획 §5.2의 문장. 신호 섹션과 접근성 이름에 그대로 쓴다. */
  sentence: string;
  /** 목록 행의 짧은 꼴. "질문 1", "미커밋 3", "턴 끝남". */
  short: string;
  /** short 에서 label 을 뺀 값 쪽. */
  value: string;
  tone: SignalTone;
  undeterminable: boolean;
  /** 판단 불가일 때의 이유(툴팁). 판단 불가가 아니면 null. */
  reason: string | null;
  /** 관측값에 붙는 보충(툴팁). 없으면 null. */
  note: string | null;
}

interface SignalOptions {
  /** 상대 날짜 표기의 기준. 주면 오늘이 아닌 시각에 날짜를 붙인다. */
  now?: number;
}

const GENERIC_REASON = '이 세션에서는 필요한 필드가 없어 판단할 수 없습니다.';
const ROW_REASON = '이 세션에서는 관측할 수 없습니다. 이유는 세션의 분석 탭에서 볼 수 있습니다.';

/** 신호마다 따로 적지 않은 이유 코드의 기본 문장. 모두 "없다는 뜻이 아니다"는 점을 분명히 한다. */
const COMMON_REASON: Record<SignalUndeterminableReason, string> = {
  pre_lineage: '구형 기록에 메시지 계보 키가 없어 활성 턴과 짝 결과를 판단할 수 없습니다. 비식별화된 기록이라는 뜻은 아닙니다.',
  pre_collect: '수집 규칙 이전에 쌓인 기록이라 필요한 필드가 없어 판단할 수 없습니다.',
  withheld: '값을 서버에 올리지 않아(보류됨) 판단할 수 없습니다. 없다는 뜻이 아닙니다.',
  dropped: '수집할 때 필드의 모양이 예상과 달라 그 값을 버렸습니다. 없다는 뜻이 아닙니다.',
  redacted: '비식별화로 값이 가려져 판단할 수 없습니다. 없다는 뜻이 아닙니다.',
  no_list: '이 세션에는 이 신호를 낼 목록이 없어 판단할 수 없습니다.',
  no_git: 'git 상태를 받지 못해 판단할 수 없습니다.',
};

const REASON_TEXT: Record<SignalKey, Partial<Record<SignalUndeterminableReason, string>>> = {
  turn: {
    pre_collect: '수집 규칙 이전에 쌓인 기록이라 마지막 턴이 끝났는지 판단할 수 없습니다.',
    redacted: '비식별화로 응답 내용이 지워져 마지막 턴이 끝났는지 판단할 수 없습니다.',
  },
  question: {
    redacted: '비식별화로 도구 입력이 지워져 답하지 않은 질문이 있는지 판단할 수 없습니다.',
  },
  background: {
    pre_collect: '수집 규칙 이전에 쌓인 기록이라 남은 백그라운드 작업을 판단할 수 없습니다.',
    redacted: '백그라운드 작업의 완료 알림 본문이 가려져 있어, 끝나지 않은 작업이 남았는지 판단할 수 없습니다.',
    withheld: '백그라운드 작업의 완료 알림 본문을 서버에 올리지 않아, 끝나지 않은 작업이 남았는지 판단할 수 없습니다.',
  },
  uncommitted: {
    no_git:
      'git 상태를 보내지 않는 클라이언트·설정이거나, 작업 폴더가 저장소가 아니거나, 편집한 저장소가 수집 허용 목록 밖이라 미커밋 파일을 판단할 수 없습니다.',
  },
  todo: {
    no_list: '이 세션은 할 일 목록을 쓰지 않아 미완료 할 일을 판단할 수 없습니다.',
    redacted: '할 일 제목이 가려져 있어 미완료 할 일을 판단할 수 없습니다.',
    withheld: '할 일 제목을 서버에 올리지 않아 미완료 할 일을 판단할 수 없습니다.',
  },
};

const reasonText = (key: SignalKey, reason: SignalUndeterminableReason | null | undefined): string =>
  (reason ? (REASON_TEXT[key][reason] ?? COMMON_REASON[reason]) : undefined) ?? GENERIC_REASON;

const TURN_LINE: Record<TurnSignalValue, { sentence: string; value: string; tone: SignalTone }> = {
  ended: { sentence: '마지막 턴 끝남', value: '끝남', tone: 'quiet' },
  open: { sentence: '턴 진행 중', value: '진행 중', tone: 'live' },
  interrupted: { sentence: '중단됨', value: '중단', tone: 'attention' },
  error: { sentence: '오류로 끝남', value: '오류', tone: 'danger' },
  no_response: { sentence: '응답 없음', value: '응답 없음', tone: 'attention' },
};

const undeterminableLine = (
  key: SignalKey,
  reason: string,
  sentence: string,
  tone: SignalTone = 'unknown',
): SignalLine => ({
  key,
  label: SIGNAL_LABEL[key],
  sentence,
  short: `${SIGNAL_LABEL[key]} ?`,
  value: '?',
  tone,
  undeterminable: true,
  reason,
  note: null,
});

const turnLine = (value: TurnSignalValue): SignalLine => {
  const t = TURN_LINE[value];
  // 계약에 없는 값이 오면 문장 대신 원문을 보인다. 빈 줄을 그리지 않는다.
  const sentence = t?.sentence ?? `턴 ${value}`;
  const short = t?.value ?? String(value);
  return {
    key: 'turn',
    label: SIGNAL_LABEL.turn,
    sentence,
    short: `${SIGNAL_LABEL.turn} ${short}`,
    value: short,
    tone: t?.tone ?? 'quiet',
    undeterminable: false,
    reason: null,
    note: null,
  };
};

/** 질문 신호. 실시간 상태가 대기이면 "응답 대기: 권한 승인"이 앞선다 (계획 §5.2). */
const questionLine = (
  count: number | null,
  waiting: WaitingKind | '' | null | undefined,
  reason: SignalUndeterminableReason | null | undefined,
  rowReason: string | null = null,
): SignalLine => {
  const subject = SIGNAL_SUBJECT.question;
  const waitingText = waiting ? `응답 대기: ${waitingKindLabel(waiting)}` : null;

  if (count === null) {
    const why = rowReason ?? reasonText('question', reason);
    // 대기 중이라는 사실은 질문 수를 몰라도 관측된 사실이다.
    if (waitingText) {
      return {
        ...undeterminableLine('question', why, `${waitingText} · ${subject} 판단 불가`, 'attention'),
        short: '응답 대기',
        value: '응답 대기',
      };
    }
    return undeterminableLine('question', why, `${subject} 판단 불가`);
  }

  const base = count > 0 ? `${subject} ${count}` : `${subject} 없음`;
  const sentence = waitingText ? (count > 0 ? `${waitingText} · ${base}` : waitingText) : base;
  const attention = count > 0 || waitingText !== null;
  const value = waitingText ? '응답 대기' : String(count);
  return {
    key: 'question',
    label: SIGNAL_LABEL.question,
    sentence,
    short: waitingText ? '응답 대기' : `${SIGNAL_LABEL.question} ${count}`,
    value,
    tone: attention ? 'attention' : 'quiet',
    undeterminable: false,
    reason: null,
    note: null,
  };
};

const countLine = (key: 'background' | 'todo', count: number): SignalLine => {
  const subject = SIGNAL_SUBJECT[key];
  return {
    key,
    label: SIGNAL_LABEL[key],
    sentence: count > 0 ? `${subject} ${count}` : `${subject} 없음`,
    short: `${SIGNAL_LABEL[key]} ${count}`,
    value: String(count),
    tone: count > 0 ? 'attention' : 'quiet',
    undeterminable: false,
    reason: null,
    note: null,
  };
};

const NOT_REEVALUATING_NOTE =
  '이 값은 마지막으로 바뀐 시점의 개수입니다. 마지막 활동이 7일을 넘었거나 수집기 신호가 없어 더는 다시 확인하지 않으므로, 이후에 커밋했어도 반영되지 않았을 수 있습니다.';
const AT_LEAST_NOTE = '편집 수집이 세션 중간부터 시작되어 일부 편집을 놓쳤을 수 있습니다. 실제 개수는 이 값 이상입니다.';
/** 편집 수집이 세션 중간부터 시작됐는데 센 개수가 0이면, 놓친 편집이 있었는지 알 수 없다(p1b-design §3.18). */
const PARTIAL_ZERO_REASON =
  '편집 수집이 세션 중간부터 시작되어 앞부분의 편집을 보지 못했습니다. 센 개수가 0이어도 미커밋 파일이 없다고 판단할 수 없습니다.';

const uncommittedLine = (
  count: number,
  extra: { atLeast?: boolean; changedAt?: string | null; reevaluating?: boolean },
  now: number | undefined,
): SignalLine => {
  const subject = SIGNAL_SUBJECT.uncommitted;
  // 시각을 읽을 수 없으면 "(알 수 없음부터)"가 되지 않도록 덧붙이지 않는다.
  const sinceAt = extra.changedAt ? formatClockNear(extra.changedAt, now ?? Date.parse(extra.changedAt)) : UNKNOWN_TEXT;
  const since = sinceAt === UNKNOWN_TEXT ? '' : ` (${sinceAt}부터)`;
  const sentence =
    count === 0
      ? `${subject} 없음${since}`
      : extra.atLeast
        ? `${subject} ${count}개 이상${since}`
        : `${subject} ${count}${since}`;
  const notes: string[] = [];
  if (extra.atLeast) notes.push(AT_LEAST_NOTE);
  if (extra.reevaluating === false) notes.push(NOT_REEVALUATING_NOTE);
  return {
    key: 'uncommitted',
    label: SIGNAL_LABEL.uncommitted,
    sentence,
    short: `${SIGNAL_LABEL.uncommitted} ${count}${extra.atLeast && count > 0 ? '+' : ''}`,
    value: `${count}${extra.atLeast && count > 0 ? '+' : ''}`,
    tone: count > 0 ? 'attention' : 'quiet',
    undeterminable: false,
    reason: null,
    note: notes.length > 0 ? notes.join(' ') : null,
  };
};

/**
 * 분석 응답의 `signals`를 문장으로 바꾼다. 키가 없는 신호는 줄이 없다. `signals` 자체가 없으면(비소유
 * 관리자) 빈 배열이다.
 */
const signalLines = (signals: SessionSignals | undefined, opts: SignalOptions = {}): SignalLine[] => {
  if (!signals) return [];
  const lines: SignalLine[] = [];
  for (const key of SIGNAL_ORDER) {
    switch (key) {
      case 'turn': {
        const s = signals.turn;
        if (!s) break;
        lines.push(
          s.state === 'observed'
            ? turnLine(s.value)
            : undeterminableLine('turn', reasonText('turn', s.reason), '턴 판단 불가'),
        );
        break;
      }
      case 'question': {
        const s = signals.question;
        if (!s) break;
        lines.push(
          s.state === 'observed'
            ? questionLine(s.value, s.waiting_kind, null)
            : questionLine(null, s.waiting_kind, s.reason),
        );
        break;
      }
      case 'background': {
        const s = signals.background;
        if (!s) break;
        lines.push(
          s.state === 'observed'
            ? countLine('background', s.value)
            : undeterminableLine('background', reasonText('background', s.reason), `${SIGNAL_SUBJECT.background} 판단 불가`),
        );
        break;
      }
      case 'uncommitted': {
        const s = signals.uncommitted;
        if (!s) break;
        const partialZero = s.state === 'observed' && s.at_least && s.value === 0;
        lines.push(
          s.state === 'observed' && !partialZero
            ? uncommittedLine(s.value, { atLeast: s.at_least, changedAt: s.changed_at, reevaluating: s.reevaluating }, opts.now)
            : undeterminableLine(
                'uncommitted',
                s.state === 'observed' ? PARTIAL_ZERO_REASON : reasonText('uncommitted', s.reason),
                `${SIGNAL_SUBJECT.uncommitted} 판단 불가`,
              ),
        );
        break;
      }
      case 'todo': {
        const s = signals.todo;
        if (!s) break;
        lines.push(
          s.state === 'observed'
            ? countLine('todo', s.value)
            : undeterminableLine('todo', reasonText('todo', s.reason), `${SIGNAL_SUBJECT.todo} 판단 불가`),
        );
        break;
      }
    }
  }
  return lines;
};

/**
 * 목록 행의 `signals`(값만 온다)를 같은 줄 모양으로 바꾼다. null 은 판단 불가, 키가 없으면 해당 없음이다.
 * 이유, `at_least`, `waiting_kind`는 목록에 오지 않는다.
 */
const rowSignalLines = (signals: SessionRowSignals | undefined): SignalLine[] => {
  if (!signals) return [];
  const lines: SignalLine[] = [];
  for (const key of SIGNAL_ORDER) {
    if (!(key in signals)) continue;
    switch (key) {
      case 'turn': {
        const v = signals.turn;
        lines.push(v == null ? undeterminableLine('turn', ROW_REASON, '턴 판단 불가') : turnLine(v));
        break;
      }
      case 'question': {
        const v = signals.question;
        lines.push(questionLine(v ?? null, null, null, ROW_REASON));
        break;
      }
      case 'background': {
        const v = signals.background;
        lines.push(
          v == null ? undeterminableLine('background', ROW_REASON, `${SIGNAL_SUBJECT.background} 판단 불가`) : countLine('background', v),
        );
        break;
      }
      case 'uncommitted': {
        const v = signals.uncommitted;
        lines.push(
          v == null
            ? undeterminableLine('uncommitted', ROW_REASON, `${SIGNAL_SUBJECT.uncommitted} 판단 불가`)
            : uncommittedLine(v, {}, undefined),
        );
        break;
      }
      case 'todo': {
        const v = signals.todo;
        lines.push(v == null ? undeterminableLine('todo', ROW_REASON, `${SIGNAL_SUBJECT.todo} 판단 불가`) : countLine('todo', v));
        break;
      }
    }
  }
  return lines;
};

export { SIGNAL_ORDER, SIGNAL_LABEL, SIGNAL_SUBJECT, signalLines, rowSignalLines, reasonText };
export type { SignalKey, SignalTone, SignalLine };

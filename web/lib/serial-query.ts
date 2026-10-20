interface QueueEntry<T> {
  signal?: AbortSignal;
  query: () => Promise<T>;
  resolve: (value: T) => void;
  reject: (reason: unknown) => void;
  cancelled: boolean;
  started: boolean;
}

/** Runs auxiliary reads one at a time and skips scopes canceled before they start. */
const createSerialQueryRunner = () => {
  const queue: QueueEntry<unknown>[] = [];
  let running = false;

  const drain = async () => {
    if (running) return;
    running = true;
    try {
      while (queue.length > 0) {
        const entry = queue.shift();
        if (!entry || entry.cancelled || entry.signal?.aborted) continue;
        entry.started = true;
        try {
          entry.resolve(await entry.query());
        } catch (error) {
          entry.reject(error);
        }
      }
    } finally {
      running = false;
      if (queue.length > 0) void drain();
    }
  };

  return <T,>(query: () => Promise<T>, signal?: AbortSignal) => new Promise<T>((resolve, reject) => {
    const entry: QueueEntry<T> = { query, signal, resolve, reject, cancelled: false, started: false };
    if (signal?.aborted) {
      reject(new DOMException('Query canceled', 'AbortError'));
      return;
    }
    signal?.addEventListener('abort', () => {
      if (entry.started) return;
      entry.cancelled = true;
      reject(new DOMException('Query canceled', 'AbortError'));
    }, { once: true });
    queue.push(entry as QueueEntry<unknown>);
    void drain();
  });
};

const runSerialSessionRead = createSerialQueryRunner();

export { createSerialQueryRunner, runSerialSessionRead };

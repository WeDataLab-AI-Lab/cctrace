import { useEffect, useState } from 'react';

interface DebouncedCallback<T> {
  (value: T): void;
  cancel: () => void;
}

const debounce = <T>(callback: (value: T) => void, delayMs: number): DebouncedCallback<T> => {
  let timeout: ReturnType<typeof setTimeout> | undefined;
  const debounced = (value: T) => {
    if (timeout !== undefined) clearTimeout(timeout);
    timeout = setTimeout(() => {
      timeout = undefined;
      callback(value);
    }, delayMs);
  };
  debounced.cancel = () => {
    if (timeout !== undefined) clearTimeout(timeout);
    timeout = undefined;
  };
  return debounced;
};

const useDebouncedValue = <T>(value: T, delayMs: number): T => {
  const [debouncedValue, setDebouncedValue] = useState(value);
  useEffect(() => {
    const update = debounce(setDebouncedValue, delayMs);
    update(value);
    return update.cancel;
  }, [value, delayMs]);
  return debouncedValue;
};

export { debounce, useDebouncedValue };

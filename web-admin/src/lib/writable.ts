type Subscriber<T> = (v: T) => void;

export type Readable<T> = {
  subscribe(run: Subscriber<T>): () => void;
};

export type Writable<T> = Readable<T> & {
  set(v: T): void;
  get(): T;
};

export function writable<T>(initial: T): Writable<T> {
  let value = initial;
  const subs = new Set<Subscriber<T>>();
  return {
    subscribe(run) {
      subs.add(run);
      run(value);
      return () => {
        subs.delete(run);
      };
    },
    set(v) {
      if (Object.is(v, value)) return;
      value = v;
      for (const s of subs) s(value);
    },
    get() {
      return value;
    },
  };
}

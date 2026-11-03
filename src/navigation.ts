import { useCallback, useEffect, useState } from "react";

/**
 * The open menu item lives in the address after "#", so the back button and a
 * pasted link both work. The hash also suits the hosted static site, which
 * does not send unknown paths to the app.
 */
export function useHashView<T extends string>(views: readonly T[], fallback: T) {
  const read = useCallback((): T => {
    const value = decodeURIComponent(window.location.hash.slice(1));
    return (views as readonly string[]).includes(value) ? value as T : fallback;
  }, [views, fallback]);
  const [view, setView] = useState<T>(read);

  useEffect(() => {
    const sync = () => setView(read());
    window.addEventListener("hashchange", sync);
    return () => window.removeEventListener("hashchange", sync);
  }, [read]);

  /** Opens a view. `replace` swaps the current history entry instead of adding one. */
  const open = useCallback((next: T, replace = false) => {
    if (replace) window.history.replaceState(null, "", `${window.location.pathname}#${next}`);
    else if (window.location.hash.slice(1) !== next) window.location.hash = next;
    setView(next);
  }, []);

  return [view, open] as const;
}

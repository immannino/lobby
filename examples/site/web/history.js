// Remembers rooms this browser has been in (code, game, role, name), so the
// play page can list them and rejoin in one click. Stored in localStorage:
// per browser, never sent to the server. Seat tokens stay in the HttpOnly
// cookie; this only keeps what's needed to find the way back.

const KEY = "lobby.history";
const MAX = 12;
const TTL = 24 * 60 * 60 * 1000; // matches the seat cookie's lifetime

function read() {
  try {
    const list = JSON.parse(localStorage.getItem(KEY) ?? "[]");
    return Array.isArray(list) ? list : [];
  } catch {
    return []; // storage blocked (private mode) or corrupted
  }
}

function write(list) {
  try {
    localStorage.setItem(KEY, JSON.stringify(list.slice(0, MAX)));
  } catch {}
}

/** Rooms from the last 24 hours, newest first. */
export function load() {
  const now = Date.now();
  const list = read().filter((e) => e && typeof e.code === "string" && now - e.at < TTL);
  write(list);
  return list;
}

/** Records (or refreshes) a room. entry: { code, game, role, name }. */
export function remember(entry) {
  const list = load().filter((e) => e.code !== entry.code);
  write([{ ...entry, at: Date.now() }, ...list]);
}

export function find(code) {
  return load().find((e) => e.code === code);
}

export function forget(code) {
  write(load().filter((e) => e.code !== code));
}

export function clear() {
  write([]);
}

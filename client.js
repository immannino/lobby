// Browser client for the lobby package. Served by Manager.Handler at /client.js.
//
//   import { connect, createRoom, joinRoom } from "/api/client.js";
//   const res = await joinRoom("/api", "BCDF", { name: "Ada" }); // sets the token cookie
//   const room = connect("/api", res.code, {
//     onMessage: (msg) => console.log(msg.type, msg.data),
//     onStatus: (status, detail) => console.log(status, detail), // "open" | "reconnecting" | "closed"
//   });
//   room.send("chat", { text: "hi" });

// The wire protocol version this client speaks (see lobby.ProtocolVersion).
export const PROTOCOL = 1;

// Close codes after which reconnecting is pointless.
export const Terminal = {
  1000: "left",
  4401: "unauthorized",
  4403: "kicked",
  4404: "room not found",
  4409: "connected elsewhere",
  4410: "room closed",
};

async function post(url, body) {
  const res = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify(body ?? {}),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`);
  return data;
}

/** Create a room and join it. body: { name } for a player, { role: "display" } for a screen. */
export const createRoom = (base, body) => post(`${base}/rooms`, body);

/** Join an existing room. body: { name } or { role: "display" }. */
export const joinRoom = (base, code, body) =>
  post(`${base}/rooms/${encodeURIComponent(code.trim().toUpperCase())}/join`, body);

/** Open a websocket to a room and keep it open, reconnecting with backoff. */
export function connect(base, code, { onMessage = () => {}, onStatus = () => {}, token } = {}) {
  let ws;
  let attempt = 0;
  let stopped = false;
  let timer;

  const url = new URL(`${base}/rooms/${encodeURIComponent(code)}/ws`, location.href);
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  if (token) url.searchParams.set("token", token);

  function open() {
    ws = new WebSocket(url);
    ws.onopen = () => {
      attempt = 0;
      onStatus("open");
    };
    ws.onmessage = (ev) => {
      let msg;
      try {
        msg = JSON.parse(ev.data);
      } catch (err) {
        console.error("lobby: bad message", err);
        return;
      }
      if (msg.type === "lobby.welcome" && msg.data?.protocol !== PROTOCOL) {
        console.warn(`lobby: server speaks protocol ${msg.data?.protocol}, this client speaks ${PROTOCOL}; reload to update`);
      }
      onMessage(msg);
    };
    ws.onclose = (ev) => {
      if (stopped) return;
      if (ev.code in Terminal) {
        stopped = true;
        onStatus("closed", { code: ev.code, reason: ev.reason || Terminal[ev.code] });
        return;
      }
      const delay = Math.min(10000, 500 * 2 ** attempt++) * (0.75 + Math.random() / 2);
      onStatus("reconnecting", { code: ev.code, delay });
      timer = setTimeout(open, delay);
    };
  }
  open();

  // Phones suspend sockets when the screen sleeps. When the page comes back
  // (or the network does), retry now instead of waiting out the backoff.
  function wake() {
    if (stopped || document.visibilityState === "hidden") return;
    if (ws.readyState === WebSocket.CLOSED && timer) {
      clearTimeout(timer);
      timer = undefined;
      attempt = 0;
      open();
    }
  }
  document.addEventListener("visibilitychange", wake);
  addEventListener("online", wake);

  return {
    send(type, data) {
      if (ws.readyState !== WebSocket.OPEN) return false;
      ws.send(JSON.stringify(data === undefined ? { type } : { type, data }));
      return true;
    },
    leave() {
      this.send("lobby.leave");
    },
    kick(id) {
      this.send("lobby.kick", { id });
    },
    lock(locked) {
      this.send("lobby.lock", { locked });
    },
    close() {
      stopped = true;
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", wake);
      removeEventListener("online", wake);
      ws.close(1000);
    },
  };
}

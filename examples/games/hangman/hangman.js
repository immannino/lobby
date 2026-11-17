// Hangman for the demo shell. The server sends each client its own "hm"
// view: only the setter's view contains the secret until the round ends.

const PARTS = [
  '<circle data-part cx="140" cy="62" r="16"/>', // head
  '<line data-part x1="140" y1="78" x2="140" y2="130"/>', // body
  '<line data-part x1="140" y1="92" x2="118" y2="112"/>', // left arm
  '<line data-part x1="140" y1="92" x2="162" y2="112"/>', // right arm
  '<line data-part x1="140" y1="130" x2="122" y2="160"/>', // left leg
  '<line data-part x1="140" y1="130" x2="158" y2="160"/>', // right leg
];

const CARD = `
  <article data-hangman>
    <hgroup>
      <h2 data-headline>Hangman</h2>
      <p data-sub></p>
    </hgroup>
    <svg data-gallows viewBox="0 0 200 200" aria-hidden="true">
      <path d="M30 185 H120 M60 185 V20 H140 V46"/>
      ${PARTS.join("")}
    </svg>
    <div data-word aria-live="polite"></div>
    <p data-secret hidden>Your word: <strong></strong></p>
    <p data-start hidden><button type="button">Start the game</button></p>
    <form data-choose hidden>
      <input name="word" maxlength="14" autocomplete="off" placeholder="Secret word" aria-label="Secret word">
      <button>Set</button>
      <button type="button" data-random>Random</button>
    </form>
    <div data-keys hidden></div>
    <form data-solve hidden>
      <input name="guess" maxlength="14" autocomplete="off" placeholder="Solve the whole word" aria-label="Solve">
      <button>Solve</button>
    </form>
  </article>`;

const LETTERS = "ABCDEFGHIJKLMNOPQRSTUVWXYZ";

export function mount({ role, room, el, before, you, state, rerender }) {
  before.insertAdjacentHTML("beforebegin", CARD);
  const card = before.parentElement.querySelector("[data-hangman]");
  const q = (sel) => card.querySelector(sel);
  const isDisplay = role === "display";
  const parts = [...card.querySelectorAll("[data-part]")];

  const keys = LETTERS.split("").map((ch) => {
    const b = el("button", { type: "button", textContent: ch });
    b.dataset.key = ch;
    b.onclick = () => room.send("guess", ch);
    return b;
  });
  q("[data-keys]").append(...keys);
  q("[data-start] button").onclick = () => room.send("start");
  q("[data-random]").onclick = () => room.send("random");
  q("[data-choose]").onsubmit = (ev) => {
    ev.preventDefault();
    const input = ev.target.elements.word;
    room.send("word", input.value);
    input.value = "";
  };
  q("[data-solve]").onsubmit = (ev) => {
    ev.preventDefault();
    const input = ev.target.elements.guess;
    if (input.value.trim()) room.send("guess", input.value);
    input.value = "";
  };

  let view = null;
  let deadline = 0;
  let ticker;
  const name = (id) => state()?.clients.find((c) => c.id === id)?.name ?? "…";
  const me = () => you()?.id;

  function describe(v) {
    const left = Math.max(0, Math.ceil((deadline - Date.now()) / 1000));
    const timer = left ? ` · ${left}s` : "";
    switch (v.phase) {
      case "waiting":
        return {
          headline: "Hangman",
          sub: isDisplay ? "Join with the code, then the VIP starts" : state()?.vip === me() ? "Start when everyone's in" : "Waiting for the VIP to start",
        };
      case "choosing":
        return v.setter === me()
          ? { headline: "Pick a secret word", sub: `3 to 14 letters${timer}` }
          : { headline: `${name(v.setter)} is picking a word`, sub: timer.slice(3) };
      case "guessing": {
        const by = v.setter ? `${name(v.setter)}'s word` : "The computer's word";
        const misses = `${v.misses}/${v.maxMisses} misses`;
        if (v.turn === me()) return { headline: "Your guess", sub: `${by} · ${misses}${timer}` };
        if (v.setter === me()) return { headline: `${name(v.turn)} is guessing your word`, sub: `${misses}${timer}` };
        return { headline: v.turn ? `${name(v.turn)}'s guess` : "Waiting for a guesser", sub: `${by} · ${misses}${timer}` };
      }
      case "over": {
        const word = v.secret ? `It was ${v.secret}` : "";
        if (v.result === "solved") return { headline: v.solver === me() ? "You solved it!" : `${name(v.solver)} solved it`, sub: word };
        if (v.result === "hanged") return { headline: "Hanged!", sub: word };
        return { headline: "Round abandoned", sub: "The setter left" };
      }
    }
    return { headline: "Hangman", sub: "" };
  }

  function draw() {
    if (!view) return;
    const v = view;
    const text = describe(v);
    q("[data-headline]").textContent = text.headline;
    q("[data-sub]").textContent = text.sub;
    const myTurn = !isDisplay && v.phase === "guessing" && v.turn === me();
    card.dataset.myTurn = myTurn ? "yes" : "no";
    card.dataset.result = v.result;

    parts.forEach((p, i) => p.toggleAttribute("data-shown", i < v.misses));
    q("[data-word]").replaceChildren(
      ...v.masked.map((ch, i) => {
        const s = el("span", { textContent: ch || " " });
        // At the end, letters nobody found show in red.
        if (v.phase === "over" && v.secret && !v.guessed.includes(v.secret[i])) s.dataset.missed = "";
        return s;
      }),
    );
    q("[data-word]").hidden = v.masked.length === 0;

    const showSecret = !isDisplay && v.setter === me() && v.secret && v.phase !== "over";
    q("[data-secret]").hidden = !showSecret;
    if (showSecret) q("[data-secret] strong").textContent = v.secret;

    q("[data-start]").hidden = isDisplay || v.phase !== "waiting" || state()?.vip !== me();
    q("[data-choose]").hidden = isDisplay || v.phase !== "choosing" || v.setter !== me();

    const guesser = !isDisplay && v.phase === "guessing" && v.setter !== me();
    q("[data-keys]").hidden = !(guesser || (isDisplay && v.phase === "guessing"));
    q("[data-solve]").hidden = !myTurn;
    for (const k of keys) {
      const guessed = v.guessed.includes(k.dataset.key);
      const hit = guessed && v.masked.includes(k.dataset.key);
      k.toggleAttribute("data-hit", hit);
      k.toggleAttribute("data-miss", guessed && !hit);
      k.disabled = !myTurn || guessed;
    }
  }

  return {
    onMessage(type, data) {
      if (type !== "hm") return;
      const prevPhase = view?.phase;
      view = data;
      deadline = data.deadline > 0 ? Date.now() + data.deadline * 1000 : 0;
      clearInterval(ticker);
      if (deadline) ticker = setInterval(draw, 500);
      draw();
      if (prevPhase !== "choosing" && data.phase === "choosing" && data.setter === me()) q("[data-choose] input").focus();
      rerender(); // scores in the player list
    },
    onState: draw,
    status: () => (view ? describe(view).headline : null),
    decoratePlayer(li, p) {
      if (!view) return;
      const pts = view.scores[p.id] ?? 0;
      const role = p.id === view.setter && view.phase !== "waiting" ? "setter" : p.id === view.turn ? "guessing" : "";
      const aside = el("small", { textContent: [role, `${pts} ${pts === 1 ? "pt" : "pts"}`].filter(Boolean).join(" · ") });
      aside.dataset.aside = "";
      li.append(aside);
    },
  };
}

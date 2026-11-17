// Shared browser module for duel games (see duel.go). It renders the
// headline, matchup, next-match countdown, "play the computer" button and
// player-list labels. A game supplies only its board:
//
//   import { mountDuel } from "./duel.js";
//   export const mount = (ctx) => mountDuel(ctx, {
//     viewType: "ttt",
//     marks: { first: "X", second: "O" },
//     board(container, { el, move }) { /* build cells */ return (view, { myTurn }) => { /* update */ }; },
//   });

const CARD = `
  <article data-duel>
    <hgroup>
      <h2 data-headline></h2>
      <p data-matchup></p>
    </hgroup>
    <div data-board></div>
    <p data-actions hidden><button data-add-bot>Play the computer</button></p>
  </article>`;

export function mountDuel({ role, room, el, before, you, state, rerender }, { viewType, marks, board }) {
  before.insertAdjacentHTML("beforebegin", CARD);
  const card = before.parentElement.querySelector("[data-duel]");
  const headline = card.querySelector("[data-headline]");
  const matchup = card.querySelector("[data-matchup]");
  const actions = card.querySelector("[data-actions]");
  const isDisplay = role === "display";
  card.querySelector("[data-add-bot]").onclick = () => room.send("bot.add");

  const update = board(card.querySelector("[data-board]"), { el, move: (data) => room.send("move", data), isDisplay });

  let view = null;
  let countdown;
  const name = (id) => state()?.clients.find((c) => c.id === id)?.name ?? "…";
  const me = () => you()?.id;

  function describe(v) {
    if (!v.first || !v.second) {
      const waiting = v.second || v.first;
      return {
        headline: waiting ? `${name(waiting)} is waiting for a challenger` : "Waiting for players",
        sub: isDisplay ? "Join with the code to play" : waiting === me() ? "Grab a friend, or play the computer" : "",
      };
    }
    const vs = `${name(v.first)} (${marks.first}) vs ${name(v.second)} (${marks.second}) · round ${v.round}`;
    if (v.winner === "draw") return { headline: "It's a draw", sub: vs };
    if (v.winner) {
      const winner = v[v.winner];
      const how = v.forfeit ? " by forfeit" : "";
      return { headline: winner === me() ? `You win${how}!` : `${name(winner)} wins${how}`, sub: vs };
    }
    if (v.toMove === me()) return { headline: "Your turn", sub: vs };
    return { headline: `${name(v.toMove)}'s turn`, sub: vs };
  }

  function draw() {
    if (!view) return;
    const v = view;
    const text = describe(v);
    headline.textContent = text.headline;
    matchup.textContent = text.sub;
    const myTurn = !isDisplay && v.toMove === me();
    card.dataset.myTurn = myTurn ? "yes" : "no";
    update(v, { myTurn, isDisplay });
    actions.hidden = isDisplay || v.bots.length > 0;

    clearInterval(countdown);
    if (v.nextAt) {
      const tick = () => {
        const left = Math.ceil((v.nextAt - Date.now()) / 1000);
        if (left > 0) matchup.textContent = `${text.sub} · next match in ${left}…`;
        else clearInterval(countdown);
      };
      tick();
      countdown = setInterval(tick, 250);
    }
  }

  return {
    onMessage(type, data) {
      if (type !== viewType) return;
      view = data;
      // Turn "in N seconds" into a deadline so redraws don't restart it.
      if (data.nextIn > 0) view.nextAt = Date.now() + data.nextIn * 1000;
      draw();
      rerender(); // the player list shows seats and scores
    },
    onState: draw, // names may have changed
    status: () => (view ? describe(view).headline : null),
    decoratePlayer(li, p) {
      if (!view) return;
      const seat = p.id === view.first ? marks.first : p.id === view.second ? marks.second : null;
      const place = view.queue.indexOf(p.id);
      const wins = view.scores[p.id] ?? 0;
      const label = seat ?? (place >= 0 ? `#${place + 1} in line` : "");
      const aside = el("small", { textContent: [view.bots.includes(p.id) && "bot", label, `${wins} ${wins === 1 ? "win" : "wins"}`].filter(Boolean).join(" · ") });
      aside.dataset.aside = "";
      li.append(aside);
    },
  };
}

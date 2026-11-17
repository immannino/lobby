// Quick polls for the demo shell. The host (VIP) writes and sends a poll;
// players vote on their phones; the big screen shows the question and,
// once visible, the distribution. Each client gets its own "poll" view:
// counts are empty until the host reveals them (or the poll is live).

const CARD = `
  <article data-poll>
    <hgroup>
      <h2 data-question>Quick poll</h2>
      <p data-sub></p>
    </hgroup>
    <ol data-options></ol>
    <p data-host-actions hidden>
      <button type="button" data-reveal>Reveal results</button>
    </p>
  </article>`;

const COMPOSER = `
  <article data-composer hidden>
    <h2 data-composer-title>Ask the room</h2>
    <form>
      <label for="poll-question">Question</label>
      <input id="poll-question" name="question" maxlength="140" autocomplete="off" placeholder="Pizza or tacos?" required>
      <fieldset data-option-inputs>
        <legend>Options</legend>
      </fieldset>
      <p><button type="button" data-add-option>Add option</button></p>
      <label><input type="checkbox" role="switch" name="live"> Show results live while people vote</label>
      <label><input type="checkbox" role="switch" name="autoReveal" checked> Reveal when everyone has voted</label>
      <p><button>Send poll</button></p>
    </form>
  </article>`;

const PAST = `
  <article data-past hidden>
    <h2>Earlier polls</h2>
    <div data-past-list></div>
  </article>`;

export function mount({ role, room, el, before, you, state, rerender }) {
  before.insertAdjacentHTML("beforebegin", CARD + COMPOSER); // results above "Ask another"
  before.insertAdjacentHTML("afterend", PAST);
  const cards = before.parentElement;
  const q = (sel) => cards.querySelector(sel);
  const card = q("[data-poll]");
  const composer = q("[data-composer]");
  const form = composer.querySelector("form");
  const isDisplay = role === "display";

  let view = null;
  let shownID = -1; // the poll the option list was built for
  const me = () => you()?.id;
  const isHost = () => !isDisplay && state()?.vip === me();
  const name = (id) => state()?.clients.find((c) => c.id === id)?.name ?? "…";
  const players = () => state()?.clients.filter((c) => c.role === "player") ?? [];

  // ---- Composer (host only) ----
  const optionInputs = q("[data-option-inputs]");
  function addOption(value = "") {
    const n = optionInputs.querySelectorAll("input").length;
    if (n >= 8) return;
    const input = el("input", { name: "option", maxLength: 60, autocomplete: "off", placeholder: `Option ${n + 1}`, value });
    input.setAttribute("aria-label", `Option ${n + 1}`);
    optionInputs.append(input);
    q("[data-add-option]").hidden = n + 1 >= 8;
    return input;
  }
  addOption();
  addOption();
  q("[data-add-option]").onclick = () => addOption()?.focus();
  form.onsubmit = (ev) => {
    ev.preventDefault();
    const data = new FormData(form);
    room.send("poll.open", {
      question: data.get("question"),
      options: data.getAll("option"),
      live: data.get("live") === "on",
      autoReveal: data.get("autoReveal") === "on",
    });
  };
  q("[data-reveal]").onclick = () => room.send("poll.reveal");

  // ---- Rendering ----
  function buildOptions(v) {
    q("[data-options]").replaceChildren(
      ...v.options.map((text, i) => {
        const label = el("span", { textContent: text });
        label.dataset.label = "";
        const count = el("span");
        count.dataset.count = "";
        const bar = el(isDisplay ? "div" : "button", { type: "button" }, label, count);
        bar.dataset.option = i;
        if (!isDisplay) bar.onclick = () => room.send("vote", i);
        return el("li", {}, bar);
      }),
    );
    shownID = v.id;
  }

  function describe(v) {
    const voted = v.voters.length;
    const tally = `${voted} of ${v.eligible} voted`;
    if (v.phase === "drafting") {
      return { sub: isHost() ? "Write a question below and send it to everyone" : `Waiting for ${name(state()?.vip)} to ask something` };
    }
    if (v.phase === "revealed") {
      return { sub: `Results · ${voted} ${voted === 1 ? "vote" : "votes"}` };
    }
    if (isDisplay) return { sub: `${tally}${v.live ? " · live" : ""}` };
    if (isHost()) return { sub: `${tally}${v.countsVisible && !v.live ? " · only you can see the results" : ""}` };
    return { sub: v.myVote >= 0 ? `You voted · ${tally}` : `Tap your answer · ${tally}` };
  }

  function draw() {
    if (!view) return;
    const v = view;
    const host = isHost();

    card.hidden = v.phase === "drafting" && !isDisplay && host;
    q("[data-question]").textContent = v.phase === "drafting" ? "Quick poll" : v.question;
    q("[data-sub]").textContent = describe(v).sub;
    card.dataset.phase = v.phase;

    if (v.phase === "drafting") {
      q("[data-options]").replaceChildren();
      shownID = -1;
    } else if (shownID !== v.id) {
      buildOptions(v);
    }

    const total = v.counts.reduce((a, b) => a + b, 0);
    const top = Math.max(0, ...v.counts);
    for (const bar of card.querySelectorAll("[data-option]")) {
      const i = Number(bar.dataset.option);
      const n = v.counts[i] ?? 0;
      const pct = total ? Math.round((n / total) * 100) : 0;
      bar.toggleAttribute("data-mine", v.myVote === i);
      bar.toggleAttribute("data-leading", v.countsVisible && total > 0 && n === top);
      bar.style.setProperty("--pct", v.countsVisible ? `${pct}%` : "0%");
      bar.querySelector("[data-count]").textContent = v.countsVisible ? `${n} · ${pct}%` : "";
      if (!isDisplay) bar.disabled = v.phase !== "open";
    }

    q("[data-host-actions]").hidden = !(host && v.phase === "open");
    composer.hidden = !(host && v.phase !== "open");
    q("[data-composer-title]").textContent = v.phase === "revealed" ? "Ask another" : "Ask the room";

    const past = q("[data-past]");
    past.hidden = v.past.length === 0 || (v.past.length === 1 && v.phase === "revealed");
    q("[data-past-list]").replaceChildren(
      ...v.past
        .slice(v.phase === "revealed" ? 1 : 0) // the newest is already on screen
        .map((p) => {
          const top = Math.max(0, ...p.counts);
          const winners = p.options.filter((_, i) => p.total > 0 && p.counts[i] === top);
          const summary = el("summary", {}, el("strong", { textContent: p.question }), el("small", { textContent: ` · ${winners.join(", ") || "no votes"}` }));
          const rows = p.options.map((o, i) => el("li", { textContent: `${o}: ${p.counts[i]}` }));
          return el("details", {}, summary, el("ul", {}, ...rows));
        }),
    );
  }

  return {
    onMessage(type, data) {
      if (type !== "poll") return;
      view = data;
      draw();
      rerender(); // "voted" marks in the player list
    },
    onState: draw, // VIP may have changed
    status: () => {
      if (!view) return null;
      if (view.phase === "drafting") return "Waiting for a question";
      return view.question;
    },
    decoratePlayer(li, p) {
      if (!view || view.phase !== "open") return;
      const aside = el("small", { textContent: view.voters.includes(p.id) ? "voted ✓" : "thinking…" });
      aside.dataset.aside = "";
      li.append(aside);
    },
  };
}

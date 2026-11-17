// Chat + shared counter. The room shell (room.html) calls mount() once and
// forwards every non-lobby message to onMessage.

const PLAYER = `
  <article>
    <div data-counter>
      <button data-add="-1" aria-label="Decrease">−</button>
      <output data-count>0</output>
      <button data-add="1" aria-label="Increase">+</button>
    </div>
  </article>
  <article>
    <h2>Chat</h2>
    <div data-chat></div>
    <form data-chat-form>
      <input name="text" maxlength="200" autocomplete="off" placeholder="Say something" aria-label="Message">
      <button>Send</button>
    </form>
  </article>`;

const DISPLAY = `
  <article>
    <h2>Counter</h2>
    <div data-counter><output data-count>0</output></div>
  </article>
  <article>
    <h2>Chat</h2>
    <div data-chat></div>
  </article>`;

export function mount({ role, room, el, before }) {
  // Static markup only; everything user-provided goes in via textContent.
  before.insertAdjacentHTML("beforebegin", role === "display" ? DISPLAY : PLAYER);
  const cards = before.parentElement;
  const count = cards.querySelector("[data-count]");
  const chat = cards.querySelector("[data-chat]");

  if (role !== "display") {
    for (const btn of cards.querySelectorAll("[data-add]")) {
      btn.onclick = () => room.send("add", Number(btn.dataset.add));
    }
    cards.querySelector("[data-chat-form]").onsubmit = (ev) => {
      ev.preventDefault();
      const input = ev.target.elements.text;
      if (input.value.trim() && room.send("chat", { text: input.value })) input.value = "";
    };
  }

  function addLine(line) {
    const div = line.system
      ? el("div", { textContent: line.text })
      : el("div", {}, el("b", { textContent: line.from }), line.text);
    if (line.system) div.dataset.system = "";
    chat.append(div);
    chat.scrollTop = chat.scrollHeight;
  }

  return {
    onMessage(type, data) {
      switch (type) {
        case "snapshot":
          count.textContent = data.count;
          chat.replaceChildren();
          for (const line of data.chat ?? []) addLine(line);
          break;
        case "count":
          count.textContent = data;
          break;
        case "chat":
          addLine(data);
          break;
      }
    },
  };
}

// Tic-tac-toe board for the demo shell. Seats, turns and results come from
// ./duel.js; this file only draws the 3×3 grid.
import { mountDuel } from "./duel.js";

export const mount = (ctx) =>
  mountDuel(ctx, {
    viewType: "ttt",
    marks: { first: "X", second: "O" },
    board(container, { el, move }) {
      container.dataset.grid = "ttt";
      const cells = Array.from({ length: 9 }, (_, i) => {
        const b = el("button", { type: "button", ariaLabel: `Cell ${i + 1}` });
        b.dataset.cell = i;
        b.onclick = () => move(i);
        return b;
      });
      container.append(...cells);

      return (v, { myTurn, isDisplay }) => {
        cells.forEach((b, i) => {
          const mark = v.board[i];
          b.textContent = mark;
          b.dataset.mark = mark;
          b.disabled = isDisplay || !myTurn || mark !== "";
          b.toggleAttribute("data-win", v.line.includes(i));
        });
      };
    },
  });

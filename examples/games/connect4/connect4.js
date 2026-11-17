// Connect 4 board for the demo shell. Seats, turns and results come from
// ./duel.js; this file only draws the 7×6 grid. Tapping any cell drops a
// disc into that column.
import { mountDuel } from "./duel.js";

const COLS = 7;
const ROWS = 6;

export const mount = (ctx) =>
  mountDuel(ctx, {
    viewType: "c4",
    marks: { first: "Red", second: "Yellow" },
    board(container, { el, move }) {
      container.dataset.grid = "c4";
      const cells = Array.from({ length: COLS * ROWS }, (_, i) => {
        const col = i % COLS;
        const b = el("button", { type: "button", ariaLabel: `Column ${col + 1}` });
        b.dataset.cell = i;
        b.dataset.col = col;
        b.onclick = () => move(col);
        return b;
      });
      container.append(...cells);

      return (v, { myTurn, isDisplay }) => {
        const full = (col) => v.board.cells[col] !== "";
        cells.forEach((b, i) => {
          const disc = v.board.cells[i];
          b.dataset.disc = disc;
          b.disabled = isDisplay || !myTurn || full(i % COLS);
          b.toggleAttribute("data-win", v.line.includes(i));
          b.toggleAttribute("data-last", v.board.last === i);
        });
      };
    },
  });

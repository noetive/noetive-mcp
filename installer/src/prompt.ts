import { createInterface } from "node:readline/promises";
import { emitKeypressEvents } from "node:readline";

/**
 * A question with a fixed set of answers.
 */
export interface Choice {
  readonly value: string;
  readonly label: string;
  readonly hint?: string;
}

/**
 * Prompter asks the person running `init` what they want.
 *
 * Declared as an interface so the interview can be tested by scripting the
 * answers rather than by driving a terminal, and so a caller with no terminal
 * never gets one at all.
 */
export interface Prompter {
  /** A free-text answer. Returns the default when the answer is empty. */
  text(question: string, options?: TextOptions): Promise<string>;
  /** A yes or no answer. */
  confirm(question: string, options?: { readonly default?: boolean }): Promise<boolean>;
  /** Exactly one of a fixed set. */
  select(question: string, choices: readonly Choice[]): Promise<string>;
  /** Any number of a fixed set, including none. */
  multiSelect(question: string, choices: readonly Choice[], selected: readonly string[]): Promise<string[]>;
}

export interface TextOptions {
  /** Used when the answer is empty, and shown in the prompt. */
  readonly default?: string;
  /** Never echoed and never left in scrollback. For the API key. */
  readonly secret?: boolean;
  /** Returns a message when the answer is unusable, or undefined when it is. */
  readonly validate?: (value: string) => string | undefined;
}

/**
 * Cancelled is thrown when the person answering presses Ctrl-C.
 *
 * A distinct type rather than a generic error because the caller has to tell an
 * abandoned install from a failed one: the first has written nothing and needs
 * no explanation, the second does.
 */
export class Cancelled extends Error {
  constructor() {
    super("cancelled");
    this.name = "Cancelled";
  }
}

/**
 * interactive reports whether there is a person on the other end to ask.
 *
 * Both streams have to be terminals. `init` runs from CI, from postinstall and
 * from editors, and in every one of those a prompt is not a question but a
 * process that never exits. CI is checked separately because some runners
 * allocate a TTY and still have nobody watching it.
 */
export function interactive(input: NodeJS.ReadStream = process.stdin, output: NodeJS.WriteStream = process.stdout): boolean {
  if (process.env.CI) return false;
  return Boolean(input.isTTY && output.isTTY);
}

/**
 * terminalPrompter asks through a real terminal.
 *
 * Built on node:readline rather than a prompt library. The published package
 * carries a signed provenance chain and one runtime dependency; a dependency
 * that only draws a checkbox list is not worth widening what a user has to
 * trust to install this.
 */
export function terminalPrompter(
  input: NodeJS.ReadStream = process.stdin,
  output: NodeJS.WriteStream = process.stdout,
): Prompter {
  return {
    async text(question, options = {}) {
      const suffix = options.default ? ` (${options.default})` : "";

      // A secret is read straight from the keypress stream, so no readline
      // interface is opened for it. Two readers on one stdin race for every
      // keystroke, and the one that loses is the one echoing the key.
      const ask = options.secret
        ? () => secret(input, output, `${question}${suffix}: `)
        : async () => {
            const rl = createInterface({ input, output, terminal: true });
            try {
              return (await rl.question(`${question}${suffix}: `)).trim();
            } finally {
              rl.close();
            }
          };

      for (;;) {
        const answer = await ask();
        const value = answer || options.default || "";

        const complaint = options.validate?.(value);
        if (!complaint) return value;

        output.write(`  ${complaint}\n`);
      }
    },

    async confirm(question, options = {}) {
      const preset = options.default ?? true;
      const rl = createInterface({ input, output, terminal: true });

      try {
        for (;;) {
          const answer = (await rl.question(`${question} ${preset ? "(Y/n)" : "(y/N)"}: `)).trim().toLowerCase();
          if (!answer) return preset;
          if (["y", "yes"].includes(answer)) return true;
          if (["n", "no"].includes(answer)) return false;

          output.write(`  Answer y or n.\n`);
        }
      } finally {
        rl.close();
      }
    },

    async select(question, choices) {
      const picked = await pick(input, output, question, choices, [choices[0]!.value], false);
      return picked[0]!;
    },

    multiSelect(question, choices, selected) {
      return pick(input, output, question, choices, selected, true);
    },
  };
}

/**
 * secret reads a line without echoing it.
 *
 * readline's own muting still leaves the typed characters in the terminal's
 * scrollback on some hosts, so the keypresses are consumed directly and nothing
 * is written back.
 */
async function secret(input: NodeJS.ReadStream, output: NodeJS.WriteStream, prompt: string): Promise<string> {
  output.write(prompt);

  return await new Promise<string>((resolve, reject) => {
    const raw = input.isTTY;
    emitKeypressEvents(input);
    if (raw) input.setRawMode(true);

    let typed = "";
    const done = (finish: () => void) => {
      input.off("keypress", onKey);
      if (raw) input.setRawMode(false);
      output.write("\n");
      finish();
    };

    const onKey = (chunk: string, key: { name?: string; ctrl?: boolean }) => {
      if (key?.ctrl && key.name === "c") return done(() => reject(new Cancelled()));
      if (key?.name === "return" || key?.name === "enter") return done(() => resolve(typed.trim()));
      if (key?.name === "backspace") {
        typed = typed.slice(0, -1);
        return;
      }
      if (chunk && !key?.ctrl) typed += chunk;
    };

    input.on("keypress", onKey);
  });
}

/**
 * pick draws a list and lets the arrow keys move through it.
 *
 * One implementation for both select and multiSelect: the two differ only in
 * whether space toggles and whether more than one entry can be marked, and a
 * second copy of the redraw and key handling is a second place for the cursor
 * arithmetic to be wrong.
 */
async function pick(
  input: NodeJS.ReadStream,
  output: NodeJS.WriteStream,
  question: string,
  choices: readonly Choice[],
  selected: readonly string[],
  multiple: boolean,
): Promise<string[]> {
  if (choices.length === 0) return [];

  const marked = new Set(multiple ? selected : []);
  let cursor = Math.max(0, choices.findIndex((c) => selected.includes(c.value)));

  const instructions = multiple
    ? "  Space to toggle, Enter to confirm.\n"
    : "  Enter to choose.\n";

  const draw = (first: boolean) => {
    if (!first) output.write(`\x1b[${choices.length}A`);
    choices.forEach((choice, i) => {
      const box = multiple ? (marked.has(choice.value) ? "[x] " : "[ ] ") : "";
      const hint = choice.hint ? `  ${choice.hint}` : "";
      output.write(`\x1b[2K${i === cursor ? ">" : " "} ${box}${choice.label}${hint}\n`);
    });
  };

  output.write(`${question}\n${instructions}`);
  draw(true);

  return await new Promise<string[]>((resolve, reject) => {
    const raw = input.isTTY;
    emitKeypressEvents(input);
    if (raw) input.setRawMode(true);

    const done = (finish: () => void) => {
      input.off("keypress", onKey);
      if (raw) input.setRawMode(false);
      finish();
    };

    const onKey = (_chunk: string, key: { name?: string; ctrl?: boolean }) => {
      switch (true) {
        case key?.ctrl && key.name === "c":
          return done(() => reject(new Cancelled()));
        case key?.name === "up":
          cursor = (cursor - 1 + choices.length) % choices.length;
          break;
        case key?.name === "down":
          cursor = (cursor + 1) % choices.length;
          break;
        case multiple && key?.name === "space": {
          const value = choices[cursor]!.value;
          if (marked.has(value)) marked.delete(value);
          else marked.add(value);
          break;
        }
        case key?.name === "return" || key?.name === "enter":
          return done(() => resolve(multiple ? choices.filter((c) => marked.has(c.value)).map((c) => c.value) : [choices[cursor]!.value]));
        default:
          return;
      }
      draw(false);
    };

    input.on("keypress", onKey);
  });
}

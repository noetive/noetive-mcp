import assert from "node:assert/strict";
import { PassThrough } from "node:stream";
import { test } from "node:test";

import { Cancelled, interactive, terminalPrompter } from "../src/prompt";

/**
 * terminal builds a pair of fake streams that look enough like a TTY for
 * readline, and collects everything written to the screen.
 *
 * `isTTY` is set but `setRawMode` is a no-op, so the keypress path runs without
 * a real terminal to put into raw mode.
 */
function terminal(): {
  input: NodeJS.ReadStream;
  output: NodeJS.WriteStream;
  written: () => string;
  type: (text: string) => void;
} {
  const input = new PassThrough() as unknown as NodeJS.ReadStream;
  const output = new PassThrough() as unknown as NodeJS.WriteStream;

  Object.assign(input, { isTTY: true, setRawMode: () => input });
  Object.assign(output, { isTTY: true, columns: 80, rows: 24 });

  const seen: string[] = [];
  output.on("data", (chunk: Buffer) => seen.push(chunk.toString()));

  return {
    input,
    output,
    written: () => seen.join(""),
    // Deferred so the prompt is already listening. Written before the reader
    // attaches, a PassThrough delivers nothing and the test hangs.
    type: (text: string) => setImmediate(() => input.write(text)),
  };
}

// Pressing Enter is how someone accepts what the installer suggested. If the
// default is not applied, an interview where every answer was accepted produces
// a server configured with nothing.
test("an empty answer takes the default", async () => {
  const t = terminal();
  const prompter = terminalPrompter(t.input, t.output);

  t.type("\n");
  assert.equal(await prompter.text("Dimensions", { default: "1024" }), "1024");
});

test("a typed answer wins over the default", async () => {
  const t = terminal();
  const prompter = terminalPrompter(t.input, t.output);

  t.type("512\n");
  assert.equal(await prompter.text("Dimensions", { default: "1024" }), "512");
});

// A rejected answer has to be asked again rather than accepted or thrown away.
// Accepting it writes a config that stops the server from starting.
test("an answer the validator rejects is asked again", async () => {
  const t = terminal();
  const prompter = terminalPrompter(t.input, t.output);

  t.type("0\n");
  const answer = prompter.text("Dimensions", {
    validate: (v) => (v === "0" ? "Not zero." : undefined),
  });
  setTimeout(() => t.input.write("1024\n"), 20);

  assert.equal(await answer, "1024");
  assert.match(t.written(), /Not zero\./);
});

// The key must not reach the screen. A prompt that echoes it puts it in the
// user's scrollback and in any screen share that is running.
test("a secret answer is never echoed", async () => {
  const t = terminal();
  const prompter = terminalPrompter(t.input, t.output);

  t.type("keyu_notOnScreen\r");
  const answer = await prompter.text("API key", { secret: true });

  assert.equal(answer, "keyu_notOnScreen");
  assert.equal(t.written().includes("keyu_notOnScreen"), false, "the key was echoed");
});

test("a yes or no question takes its default when nothing is typed", async () => {
  for (const preset of [true, false]) {
    const t = terminal();
    const prompter = terminalPrompter(t.input, t.output);

    t.type("\n");
    assert.equal(await prompter.confirm("Close it?", { default: preset }), preset);
  }
});

test("y and n are both understood, in either case", async () => {
  for (const [typed, expected] of [["y", true], ["YES", true], ["n", false], ["No", false]] as const) {
    const t = terminal();
    const prompter = terminalPrompter(t.input, t.output);

    t.type(`${typed}\n`);
    assert.equal(await prompter.confirm("Close it?", { default: !expected }), expected);
  }
});

// An answer that is neither has to be asked again. Reading "maybe" as the
// default silently decides something the user was trying to think about.
test("an answer that is neither yes nor no is asked again", async () => {
  const t = terminal();
  const prompter = terminalPrompter(t.input, t.output);

  t.type("maybe\n");
  const answer = prompter.confirm("Close it?", { default: false });
  setTimeout(() => t.input.write("y\n"), 20);

  assert.equal(await answer, true);
  assert.match(t.written(), /Answer y or n/);
});

test("a list returns the entry the cursor is on", async () => {
  const t = terminal();
  const prompter = terminalPrompter(t.input, t.output);

  // Down once, then Enter.
  t.type("\x1b[B\r");
  assert.equal(
    await prompter.select("Which editor?", [
      { value: "cursor", label: "Cursor" },
      { value: "kiro", label: "Kiro" },
    ]),
    "kiro",
  );
});

// Wrapping matters because the list is short and the arrow key is held down.
// Without it the cursor sticks at the top and the first entry cannot be left.
test("the cursor wraps at both ends of a list", async () => {
  const t = terminal();
  const prompter = terminalPrompter(t.input, t.output);

  // Up from the first entry lands on the last.
  t.type("\x1b[A\r");
  assert.equal(
    await prompter.select("Which editor?", [
      { value: "cursor", label: "Cursor" },
      { value: "kiro", label: "Kiro" },
      { value: "codex", label: "Codex" },
    ]),
    "codex",
  );
});

test("space toggles an entry in a multiple choice list", async () => {
  const t = terminal();
  const prompter = terminalPrompter(t.input, t.output);

  const choices = [
    { value: "doctor", label: "doctor" },
    { value: "semql", label: "semql" },
    { value: "semantik", label: "semantik" },
  ];

  // Turn the first one off, move down twice, turn the third one on.
  t.type(" \x1b[B\x1b[B \r");
  assert.deepEqual(await prompter.multiSelect("Skills", choices, ["doctor", "semql"]), ["semql", "semantik"]);
});

// Choosing nothing is a legitimate answer, and one someone will give. It must
// not be read as "they did not answer, so install everything".
test("deselecting everything returns nothing", async () => {
  const t = terminal();
  const prompter = terminalPrompter(t.input, t.output);

  t.type(" \r");
  assert.deepEqual(await prompter.multiSelect("Skills", [{ value: "doctor", label: "doctor" }], ["doctor"]), []);
});

// Ctrl-C during the interview has to unwind rather than resolve. Resolving
// would carry on and write a config out of half-collected answers.
test("ctrl-c cancels rather than answering", async () => {
  const t = terminal();
  const prompter = terminalPrompter(t.input, t.output);

  t.type("\x03");
  await assert.rejects(() => prompter.multiSelect("Skills", [{ value: "doctor", label: "doctor" }], []), Cancelled);
});

test("ctrl-c cancels a secret prompt too", async () => {
  const t = terminal();
  const prompter = terminalPrompter(t.input, t.output);

  t.type("\x03");
  await assert.rejects(() => prompter.text("API key", { secret: true }), Cancelled);
});

// An empty list would otherwise draw a prompt with nothing under it and wait
// for a key that decides nothing.
test("a list with no entries returns immediately", async () => {
  const t = terminal();
  const prompter = terminalPrompter(t.input, t.output);

  assert.deepEqual(await prompter.multiSelect("Skills", [], []), []);
});

// This is the guard that keeps init usable from postinstall, CI and an editor.
// A prompt in any of those is a process that never exits.
test("nothing is interactive without a terminal on both ends", () => {
  const tty = { isTTY: true } as NodeJS.ReadStream;
  const pipe = { isTTY: false } as NodeJS.ReadStream;

  const previousCI = process.env.CI;
  delete process.env.CI;
  try {
    assert.equal(interactive(tty, tty as unknown as NodeJS.WriteStream), true);
    assert.equal(interactive(pipe, tty as unknown as NodeJS.WriteStream), false);
    assert.equal(interactive(tty, pipe as unknown as NodeJS.WriteStream), false);

    // Some runners allocate a terminal and still have nobody watching it.
    process.env.CI = "true";
    assert.equal(interactive(tty, tty as unknown as NodeJS.WriteStream), false);
  } finally {
    if (previousCI === undefined) delete process.env.CI;
    else process.env.CI = previousCI;
  }
});

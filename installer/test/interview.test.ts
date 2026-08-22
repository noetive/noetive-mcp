import assert from "node:assert/strict";
import { test } from "node:test";

import { interview, SuggestedDimensions, SuggestedModel } from "../src/interview";
import type { Choice, Prompter, TextOptions } from "../src/prompt";

interface Asked {
  readonly kind: "text" | "confirm" | "select" | "multiSelect";
  readonly question: string;
}

/**
 * scripted answers questions from a lookup keyed by a fragment of the question,
 * and records the order they were asked in.
 *
 * Keyed on a fragment rather than the exact wording so a reworded prompt does
 * not fail every test in this file, and so each test states only the answers it
 * cares about. Anything unscripted takes the default, which is what a person
 * pressing Enter through the interview would get.
 */
function scripted(answers: Record<string, string | boolean | string[]>): { prompter: Prompter; asked: Asked[] } {
  const asked: Asked[] = [];

  // Case-sensitive, because the prompts mention each other: the model question
  // reads "the model that namespace is provisioned with", and matching that
  // loosely answers it with the namespace.
  const find = (question: string) => {
    const key = Object.keys(answers).find((k) => question.includes(k));
    return key === undefined ? undefined : answers[key];
  };

  const prompter: Prompter = {
    async text(question: string, options: TextOptions = {}) {
      asked.push({ kind: "text", question });
      const answer = find(question);
      const value = typeof answer === "string" ? answer : (options.default ?? "");

      // The interview's validators are part of what is being tested, so a
      // scripted answer goes through them exactly as a typed one would.
      const complaint = options.validate?.(value);
      if (complaint) throw new Error(`the interview rejected the scripted answer ${JSON.stringify(value)}: ${complaint}`);

      return value;
    },
    async confirm(question: string, options = {}) {
      asked.push({ kind: "confirm", question });
      const answer = find(question);
      return typeof answer === "boolean" ? answer : (options.default ?? true);
    },
    async select(question: string, choices: readonly Choice[]) {
      asked.push({ kind: "select", question });
      const answer = find(question);
      return typeof answer === "string" ? answer : choices[0]!.value;
    },
    async multiSelect(question: string, _choices: readonly Choice[], selected: readonly string[]) {
      asked.push({ kind: "multiSelect", question });
      const answer = find(question);
      return Array.isArray(answer) ? answer : [...selected];
    },
  };

  return { prompter, asked };
}

const editor = { expandsVariables: true, offeredSkills: [{ value: "semql", label: "semql" }] };

// The point of the interview. Before it, someone who did not already know the
// flag names installed a server that refused every call until they read the
// README and ran the command again.
test("the interview collects every setting the server reads", async () => {
  const { prompter } = scripted({
    "Write your API key": true,
    "API key from": "keyu_3xAmPl3",
    Namespace: "acme-platform",
    "Embedding model": "Qwen3-Embedding-4B",
    Dimensions: "1024",
    "Close the shared": true,
  });

  const answers = await interview(prompter, editor);

  assert.equal(answers.apiKey, "keyu_3xAmPl3");
  assert.equal(answers.namespace, "acme-platform");
  assert.equal(answers.model, "Qwen3-Embedding-4B");
  assert.equal(answers.dimensions, "1024");
  assert.equal(answers.disableGlobalNamespace, true);
});

// Someone who spelled out every flag has already said what they want. Asking
// again turns a scripted install into an interactive one, which is how a
// documented one-liner becomes a command that hangs.
test("a question whose flag was passed is not asked", async () => {
  const { prompter, asked } = scripted({});

  const answers = await interview(prompter, {
    ...editor,
    apiKey: "keyu_fromflag",
    namespace: "from-flag",
    model: "model-from-flag",
    dimensions: "512",
    disableGlobalNamespace: false,
    skills: ["semql"],
  });

  assert.deepEqual(asked, [], "nothing should have been asked");
  assert.equal(answers.apiKey, "keyu_fromflag");
  assert.equal(answers.namespace, "from-flag");
  assert.equal(answers.disableGlobalNamespace, false);
  assert.deepEqual(answers.skills, ["semql"]);
});

// Model and dimensions describe how a namespace embeds. Collected against no
// namespace they are two values that will never be used together, and the
// prompt for them implies a decision that has not been made.
test("model and dimensions are not asked for when no namespace was named", async () => {
  const { prompter, asked } = scripted({ Namespace: "" });

  const answers = await interview(prompter, editor);

  assert.equal(answers.namespace, undefined);
  assert.equal(answers.model, undefined);
  assert.equal(answers.dimensions, undefined);
  assert.equal(
    asked.some((a) => a.question.includes("Embedding model")),
    false,
    "the model should not have been asked for",
  );
});

// The shared namespace spans tenants and is the example value in every piece of
// guidance an agent reads, which makes it what an agent reaches for when it is
// unsure. Defaulting a fresh install to leaving it open is a leak nobody chose.
test("closing the shared namespace is the default answer", async () => {
  const { prompter } = scripted({});

  const answers = await interview(prompter, editor);

  assert.equal(answers.disableGlobalNamespace, true);
});

// The answer is written whichever way it went, so an exported variable
// elsewhere cannot close a namespace the user asked to keep open.
test("leaving the shared namespace open is recorded, not omitted", async () => {
  const { prompter } = scripted({ "Close the shared": false });

  const answers = await interview(prompter, editor);

  assert.equal(answers.disableGlobalNamespace, false);
});

// The suggested pair is what the shared namespace is provisioned with, so
// pressing Enter through the interview produces a server that can actually make
// a call rather than one configured with nothing.
test("pressing enter takes the suggested model and dimensions", async () => {
  const { prompter } = scripted({ Namespace: "acme-platform" });

  const answers = await interview(prompter, editor);

  assert.equal(answers.model, SuggestedModel);
  assert.equal(answers.dimensions, SuggestedDimensions);
});

// A typo here surfaces as a server that will not start, reported into a log the
// editor collects and nobody reads. Catching it while the person is still at
// the keyboard is the only place it can be fixed cheaply.
test("an unusable dimensionality is refused at the prompt", async () => {
  for (const bad of ["0", "-1", "70000", "1024d", "1.5"]) {
    const { prompter } = scripted({ Namespace: "acme-platform", Dimensions: bad });

    await assert.rejects(
      () => interview(prompter, editor),
      /rejected the scripted answer/,
      `${bad} should have been refused`,
    );
  }
});

// An editor that does not expand variables gets no offer to reference one:
// writing ${NOETIVE_KEY_SECRET} into a config that will never substitute it
// produces "unauthorized" and sends the user to check an account that is fine.
test("an editor that cannot expand variables is asked for the key directly", async () => {
  const { prompter, asked } = scripted({ "API key from": "keyu_literal" });

  const answers = await interview(prompter, { ...editor, expandsVariables: false });

  assert.equal(answers.apiKey, "keyu_literal");
  assert.equal(
    asked.some((a) => a.question.includes("Write your API key")),
    false,
    "an editor that cannot expand should not be offered the reference",
  );
});

// Pasting the variable rather than the key is a mistake that otherwise survives
// all the way to an "unauthorized" from the server.
test("a variable reference pasted as the key is refused", async () => {
  const { prompter } = scripted({ "Write your API key": true, "API key from": "${NOETIVE_KEY_SECRET}" });

  await assert.rejects(() => interview(prompter, editor), /rejected the scripted answer/);
});

// The default is to reference the variable rather than write the key, which is
// what keeps the secret out of a file that gets synced or committed.
test("declining to embed the key writes no key", async () => {
  const { prompter } = scripted({});

  const answers = await interview(prompter, editor);

  assert.equal(answers.apiKey, undefined);
});

// An editor with nowhere to put skills must not be asked which ones it wants.
test("no skill question is asked when the editor has nowhere to put them", async () => {
  const { prompter, asked } = scripted({});

  const answers = await interview(prompter, { ...editor, offeredSkills: [] });

  assert.deepEqual(answers.skills, []);
  assert.equal(asked.some((a) => a.kind === "multiSelect"), false);
});

// Order is load-bearing: someone who stops answering partway through should
// still have a server that authenticates, so the credential comes first and the
// things a first install can skip come last.
test("the interview asks for the key before anything else", async () => {
  const { prompter, asked } = scripted({ "Write your API key": true, "API key from": "keyu_x", Namespace: "n" });

  await interview(prompter, editor);

  const questions = asked.map((a) => a.question);
  assert.ok(questions[0]!.includes("API key"), `expected the key first, got: ${questions[0]}`);
  assert.ok(
    questions.findIndex((q) => q.includes("Namespace")) < questions.findIndex((q) => q.includes("Close the shared")),
    "expected the namespace before the shared-namespace question",
  );
});

import assert from "node:assert/strict";
import { test } from "node:test";

import { interview, keyComplaint, SuggestedDimensions, SuggestedModel, withDefaults } from "../src/interview";
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
    "API key from": "keya_3xAmPl3",
    Namespace: "acme-platform",
    "Embedding model": "Qwen3-Embedding-4B",
    Dimensions: "1024",
    "Close the shared": true,
  });

  const answers = await interview(prompter, editor);

  assert.equal(answers.apiKey, "keya_3xAmPl3");
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
    apiKey: "keya_fromflag",
    namespace: "from-flag",
    model: "model-from-flag",
    dimensions: "512",
    disableGlobalNamespace: false,
    skills: ["semql"],
  });

  assert.deepEqual(asked, [], "nothing should have been asked");
  assert.equal(answers.apiKey, "keya_fromflag");
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
// unsure. Once the user names their own, leaving it open is a leak nobody chose.
test("closing the shared namespace is the default answer once a namespace is named", async () => {
  const { prompter } = scripted({ "Namespace for calls": "acme" });

  const answers = await interview(prompter, editor);

  assert.equal(answers.disableGlobalNamespace, true);
});

// Pressing Enter on the namespace takes the shared one. Asking next whether to
// close it would offer an answer that refuses every call the install routes.
test("the close question is not asked while the shared namespace is in use", async () => {
  const { prompter, asked } = scripted({});

  const answers = await interview(prompter, editor);

  assert.equal(answers.namespace, "global");
  assert.equal(answers.disableGlobalNamespace, false);
  assert.equal(asked.some((a) => a.question.startsWith("Close the shared")), false);
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
  const { prompter, asked } = scripted({ "API key from": "keya_literal" });

  const answers = await interview(prompter, { ...editor, expandsVariables: false });

  assert.equal(answers.apiKey, "keya_literal");
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
  const { prompter, asked } = scripted({ "Write your API key": true, "API key from": "keya_x", Namespace: "n" });

  await interview(prompter, editor);

  const questions = asked.map((a) => a.question);
  assert.ok(questions[0]!.includes("API key"), `expected the key first, got: ${questions[0]}`);
  assert.ok(
    questions.findIndex((q) => q.includes("Namespace")) < questions.findIndex((q) => q.includes("Close the shared")),
    "expected the namespace before the shared-namespace question",
  );
});

// Pasting the key is fine; pasting the name of the variable that should have
// held it is the mistake, in either shell syntax.
test("a variable reference is not a key, and a key or an empty value is not a complaint", () => {
  assert.ok(keyComplaint("${NOETIVE_KEY_SECRET}"));
  assert.ok(keyComplaint("%NOETIVE_KEY_SECRET%"));
  assert.equal(keyComplaint("keya_abc"), undefined);
  assert.equal(keyComplaint(""), undefined);
});

// An agent's shell gets withDefaults; a person in a terminal pressing Enter
// gets the interview's suggestions. The two must be the same install, or the
// README prompt and the terminal install quietly configure different things.
// Checked across every combination of what the flags can leave open.
test("pressing Enter through the interview gives the same install as no terminal", async () => {
  const offered = [[], [{ value: "semql", label: "semql" }]];

  for (const expandsVariables of [true, false]) {
    for (const offeredSkills of offered) {
      for (const namespace of [undefined, "", "acme", "GLOBAL"]) {
        for (const disableGlobalNamespace of [undefined, false]) {
          const given = {
            expandsVariables,
            offeredSkills,
            ...(namespace === undefined ? {} : { namespace }),
            ...(disableGlobalNamespace === undefined ? {} : { disableGlobalNamespace }),
          };
          const label = JSON.stringify({ expandsVariables, skills: offeredSkills.length, namespace, disableGlobalNamespace });

          const { prompter } = scripted({});
          assert.deepEqual(await interview(prompter, given), withDefaults(given).answers, label);
        }
      }
    }
  }
});

// The list is what tells the user what they did not choose. Each setting the
// flags left open appears exactly once, and a setting that was passed does not.
test("withDefaults lists what it filled in and nothing that was passed", () => {
  const editor = { expandsVariables: true, offeredSkills: [{ value: "semql", label: "semql" }] };

  const bare = withDefaults(editor).defaulted.map((d) => d.setting);
  assert.deepEqual(bare, ["namespace", "model", "dimensions", "shared namespace", "skills"]);

  const given = withDefaults({ ...editor, namespace: "acme", model: "m", skills: [] }).defaulted.map((d) => d.setting);
  assert.deepEqual(given, ["dimensions", "shared namespace"]);
});

// Only the shared namespace carries a warning. Warning about every default
// buries the one that leaks work across tenants.
test("only a defaulted shared namespace is warned about", () => {
  const { defaulted } = withDefaults({ expandsVariables: true, offeredSkills: [] });

  assert.deepEqual(defaulted.filter((d) => d.warning).map((d) => d.setting), ["namespace"]);
  assert.equal(withDefaults({ expandsVariables: true, offeredSkills: [], namespace: "acme" }).defaulted.some((d) => d.warning), false);
});

// The key is typed into a terminal that may be screen-shared or recorded. It
// has to be read without echo, which only the prompt's options can say.
test("the key prompt hides what is typed", async () => {
  const seen: { question: string; secret: boolean }[] = [];
  const { prompter } = scripted({});
  const recording: Prompter = {
    ...prompter,
    async text(question: string, options: TextOptions = {}) {
      seen.push({ question, secret: options.secret === true });
      return prompter.text(question, options);
    },
  };

  await interview(recording, { ...editor, expandsVariables: false });

  const key = seen.find((s) => s.question.startsWith("API key from"));
  assert.ok(key, "the key was never asked for");
  assert.equal(key.secret, true, "the key prompt echoes what is typed");
});

// Pressing Enter on "write your key into the config?" is "no". Asking for the
// key anyway invites it into a file the user just declined to put it in.
test("declining to embed the key does not then ask for it", async () => {
  const { prompter, asked } = scripted({});

  await interview(prompter, editor);

  assert.equal(asked.some((a) => a.question.startsWith("API key from")), false);
});

// The server accepts 1 to 65535 and refuses to start outside it. Both ends are
// real dimensionalities someone could configure, and both neighbours are typos
// that would otherwise surface as a server that will not start.
test("dimensions from 1 to 65535 are accepted and nothing outside", async () => {
  for (const value of ["1", "65535"]) {
    const { prompter } = scripted({ "Namespace for calls": "acme", Dimensions: value });
    assert.equal((await interview(prompter, editor)).dimensions, value);
  }
  for (const value of ["0", "65536"]) {
    const { prompter } = scripted({ "Namespace for calls": "acme", Dimensions: value });
    await assert.rejects(() => interview(prompter, editor), /rejected the scripted answer/, `${value} was accepted`);
  }
});

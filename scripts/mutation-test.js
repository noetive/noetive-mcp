#!/usr/bin/env node
"use strict";

// Mutation testing for both languages in this repository.
//
// Coverage says a line ran. It does not say anything about whether a test would
// notice if that line were wrong — a suite can execute every branch and assert
// nothing that matters. Mutation testing asks the only question that counts:
// break the implementation on purpose, and does the suite go red?
//
// A surviving mutant is a hole. Either the behaviour it broke is untested, or
// the test that covers it asserts something too weak to notice.
//
//     node scripts/mutation-test.js              # everything
//     node scripts/mutation-test.js --lang go    # one language
//     node scripts/mutation-test.js --file internal/broker/subscribe.go
//     node scripts/mutation-test.js --list       # show mutants without running
//
// Runs on a scratch copy of each file and restores it in a finally block, so an
// interrupted run cannot leave a mutant behind.

const { execFileSync, execSync } = require("node:child_process");
const { existsSync, readFileSync, rmSync, writeFileSync } = require("node:fs");
const { join, relative } = require("node:path");

const ROOT = join(__dirname, "..");

// Where the pre-mutation contents are parked while a mutant is on disk.
const SALVAGE = join(ROOT, ".mutation-salvage.json");

/**
 * restoreInterrupted puts back a file left mutated by a run that was killed.
 *
 * A finally block does not survive SIGKILL, so without this a stopped run
 * leaves a deliberate bug in the working tree with nothing to announce it — and
 * the next person to run the tests sees a failure, or a hang, that has no
 * apparent cause.
 */
function restoreInterrupted() {
  if (!existsSync(SALVAGE)) return;

  const { file, original } = JSON.parse(readFileSync(SALVAGE, "utf8"));
  writeFileSync(join(ROOT, file), original);
  rmSync(SALVAGE, { force: true });
  console.log(`Restored ${file}, left mutated by an interrupted run.\n`);
}

// Each operator states the failure it simulates. An operator nobody can explain
// produces survivors nobody can act on.
const OPERATORS = [
  {
    id: "boundary",
    reason: "an off-by-one in a comparison, which is how a limit becomes exclusive when it should be inclusive",
    rules: [
      { find: /([^<>=!])<=/g, replace: "$1<" },
      { find: /([^<>=!])>=/g, replace: "$1>" },
      { find: /([^<>=!])<([^=-])/g, replace: "$1<=$2" },
      { find: /([^<>=!])>([^=])/g, replace: "$1>=$2" },
    ],
  },
  {
    id: "negate",
    reason: "an inverted condition, which is how a guard comes to admit exactly what it was meant to refuse",
    rules: [
      { find: /([^!<>])==/g, replace: "$1!=" },
      { find: /!=/g, replace: "==" },
    ],
  },
  {
    id: "logic",
    reason: "a loosened or tightened condition, which is how a two-part check starts passing on one part",
    rules: [
      { find: /&&/g, replace: "||" },
      { find: /\|\|/g, replace: "&&" },
    ],
  },
  {
    id: "boolean",
    reason: "an inverted flag, which is how a reported outcome stops matching what happened",
    rules: [
      { find: /\btrue\b/g, replace: "false" },
      { find: /\bfalse\b/g, replace: "true" },
    ],
  },
  {
    id: "emptiness",
    reason: "a dropped emptiness check, which is how a blank value passes for a real one",
    rules: [
      { find: /== ""/g, replace: '!= "___never___"' },
      { find: /!= ""/g, replace: '== "___never___"' },
    ],
  },
];

/**
 * sourceFiles lists files git knows about — tracked or newly added but not
 * ignored — that still exist on disk.
 *
 * Both halves matter: listing only tracked files misses a package that has not
 * been committed yet, and not checking existence tries to mutate files that
 * were deleted in the working tree.
 */
function sourceFiles(predicate) {
  return execSync("git ls-files --cached --others --exclude-standard", { cwd: ROOT, encoding: "utf8" })
    .split("\n")
    .filter((f) => f && predicate(f) && existsSync(join(ROOT, f)));
}

const TARGETS = {
  go: {
    // Source only. Mutating a test file proves nothing about the suite.
    files: () => sourceFiles((f) => f.endsWith(".go") && !f.endsWith("_test.go")),
    // -failfast stops at the first red test: a mutant is killed the moment
    // anything notices, and the rest of the run adds nothing.
    test: () => runTests("go", ["test", "-failfast", "-count=1", "./..."]),
    comment: /^\s*\/\//,
  },
  ts: {
    files: () => sourceFiles((f) => f.startsWith("installer/src/") && f.endsWith(".ts")),
    test: () => runTests("npm", ["test"], join(ROOT, "installer")),
    comment: /^\s*(\/\/|\*|\/\*)/,
  },
};

function main() {
  const args = parseArgs(process.argv.slice(2));
  const languages = args.lang ? [args.lang] : Object.keys(TARGETS);

  const mutants = [];
  for (const lang of languages) {
    const target = TARGETS[lang];
    if (!target) fail(`unknown language ${lang}; expected one of ${Object.keys(TARGETS).join(", ")}`);

    for (const file of target.files()) {
      if (args.file && !file.includes(args.file)) continue;
      mutants.push(...mutantsIn(lang, file, target.comment));
    }
  }

  if (args.list) {
    for (const m of mutants) console.log(`${m.file}:${m.line}  ${m.id}  ${m.before.trim()}  ->  ${m.after.trim()}`);
    console.log(`\n${mutants.length} mutants`);
    return;
  }

  console.log(`Running ${mutants.length} mutants across ${languages.join(" and ")}.\n`);

  const survivors = [];
  let killed = 0;

  // A finally block does not run when the process is killed, and a mutant left
  // on disk is far worse than an incomplete report: it is a deliberate bug in
  // the working tree that nothing announces. The original is parked in a file
  // first, and any run that starts finds it and puts it back.
  restoreInterrupted();

  for (const [index, mutant] of mutants.entries()) {
    const target = join(ROOT, mutant.file);
    const original = readFileSync(target, "utf8");
    let outcome;
    try {
      writeFileSync(SALVAGE, JSON.stringify({ file: mutant.file, original }));
      writeFileSync(target, applyAt(original, mutant));
      outcome = TARGETS[mutant.lang].test();
    } finally {
      writeFileSync(target, original);
      rmSync(SALVAGE, { force: true });
    }

    if (outcome === "killed") {
      killed += 1;
    } else if (outcome === "survived") {
      survivors.push(mutant);
    }
    // "invalid" mutants do not compile and are neither killed nor survivors:
    // they are not a hypothesis about behaviour, just broken syntax.

    process.stdout.write(
      `\r  ${index + 1}/${mutants.length}  killed ${killed}  survived ${survivors.length}   `,
    );
  }

  report(mutants, killed, survivors);
  process.exit(survivors.length > 0 ? 1 : 0);
}

/** mutantsIn enumerates every mutation an operator can make in one file. */
function mutantsIn(lang, file, commentPattern) {
  const lines = readFileSync(join(ROOT, file), "utf8").split("\n");
  const mutants = [];

  // A backtick string — a JS template literal or a Go raw string — runs across
  // lines, so its interior looks like ordinary code to a line-at-a-time scan.
  // Usage text and embedded schemas live in exactly those, and mutating prose
  // produces survivors that are real only in the report.
  let insideMultilineString = false;

  for (const [index, line] of lines.entries()) {
    const backticks = (line.match(/`/g) ?? []).length;
    const opensOrCloses = backticks % 2 === 1;

    if (insideMultilineString) {
      if (opensOrCloses) insideMultilineString = false;
      continue;
    }
    if (opensOrCloses) {
      insideMultilineString = true;
      continue;
    }

    // A comment is prose. Mutating it changes nothing a test could detect, so
    // every such mutant would be a false survivor.
    if (commentPattern.test(line) || line.trim() === "") continue;

    for (const operator of OPERATORS) {
      for (const rule of operator.rules) {
        const mutated = replaceOutsideStrings(line, rule);
        if (mutated === line) continue;

        mutants.push({
          lang,
          file,
          line: index + 1,
          id: operator.id,
          reason: operator.reason,
          before: line,
          after: mutated,
        });
      }
    }
  }

  return mutants;
}

/**
 * replaceOutsideStrings applies a rule to code only, leaving string literals
 * alone.
 *
 * Without this, a message containing `<editor>` gets mutated to `<=editor>` and
 * survives every test — correctly, because changing prose changes no behaviour.
 * Those false survivors are worse than no report: they bury the real holes in
 * noise, and a report nobody trusts is a report nobody reads.
 */
function replaceOutsideStrings(line, rule) {
  // Splitting on a capturing group interleaves the captures into the result,
  // so the array alternates code, literal, code, literal. Only the even
  // positions are code.
  return line
    .split(STRING_LITERAL)
    .map((part, i) => (i % 2 === 0 ? part.replace(rule.find, rule.replace) : part))
    .join("");
}

// Double-quoted, single-quoted, backtick and Go raw strings, each allowing
// escaped quotes within.
const STRING_LITERAL = /("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|`[^`]*`)/;

function applyAt(source, mutant) {
  const lines = source.split("\n");
  lines[mutant.line - 1] = mutant.after;
  return lines.join("\n");
}

/**
 * runTests reports whether the suite noticed. A compile failure is "invalid"
 * rather than "killed": a mutant that does not build never expressed a
 * behaviour a test could have caught, and counting it as a kill inflates the
 * score with mutants that proved nothing.
 */
function runTests(command, args, cwd = ROOT) {
  try {
    execFileSync(command, args, {
      cwd,
      encoding: "utf8",
      stdio: "pipe",
      env: { ...process.env, ...GO_ENV },
      // A mutant can turn a loop condition into one that never terminates.
      // Without a bound the whole run hangs on it, and the timeout doubles as
      // the kill signal: a suite that no longer finishes has noticed.
      timeout: TEST_TIMEOUT_MS,
    });
    return "survived";
  } catch (err) {
    const output = `${err.stdout ?? ""}${err.stderr ?? ""}`;
    return compileFailure(output) ? "invalid" : "killed";
  }
}

// Generous against a slow machine, tight enough that a non-terminating mutant
// costs one interval rather than the rest of the run.
const TEST_TIMEOUT_MS = 120_000;

const GO_ENV = {
  // The Semantik SDK is not on the public proxy; these keep an offline run
  // resolving from the local module cache instead of failing every mutant with
  // a network error that would read as a kill.
  GOFLAGS: "-mod=mod",
  GOPRIVATE: "example.invalid",
  GOPROXY: process.env.GOPROXY ?? "off",
  GOSUMDB: "off",
};

function compileFailure(output) {
  return (
    /\[build failed\]|cannot use|undefined:|syntax error|declared and not used|too many|not enough/.test(output) ||
    /error TS\d+/.test(output)
  );
}

function report(mutants, killed, survivors) {
  const scored = killed + survivors.length;
  const score = scored === 0 ? 0 : Math.round((killed / scored) * 100);

  console.log(`\n\n${killed} killed, ${survivors.length} survived, ${mutants.length - scored} did not compile.`);
  console.log(`Mutation score: ${score}% of ${scored} viable mutants.\n`);

  if (survivors.length === 0) {
    console.log("No survivors: every viable mutant was caught by a test.");
    return;
  }

  console.log("Survivors — each is a change no test noticed:\n");
  for (const s of survivors) {
    console.log(`  ${relative(".", s.file)}:${s.line}  [${s.id}]`);
    console.log(`    ${s.before.trim()}`);
    console.log(`    ${s.after.trim()}`);
    console.log(`    misses: ${s.reason}\n`);
  }
}

function parseArgs(argv) {
  const args = {};
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--list") args.list = true;
    else if (arg === "--lang") args.lang = argv[++i];
    else if (arg === "--file") args.file = argv[++i];
    else fail(`unknown argument ${arg}`);
  }
  return args;
}

function fail(message) {
  console.error(`mutation-test: ${message}`);
  process.exit(2);
}

main();

import assert from "node:assert/strict";
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, symlinkSync, writeFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { parse, run, useBinaryResolver, usePrompter } from "../src/cli";
import { resolveBinary } from "../src/resolveBinary";
import { terminalPrompter } from "../src/prompt";
import { clientIds, clientSpec } from "../src/clients";

/** capture runs a command and collects everything it wrote. */
async function capture(argv: string[]): Promise<{ code: number; out: string; err: string }> {
  const out: string[] = [];
  const err: string[] = [];
  const code = await run(
    argv,
    (line) => out.push(line),
    (line) => err.push(line),
  );
  return { code, out: out.join("\n"), err: err.join("\n") };
}

/**
 * withHome runs body against a scratch home directory containing exactly the
 * given editor markers, so which editors are detected is a property of the test
 * rather than of whatever happens to be installed on the machine running it.
 *
 * Both variables, because os.homedir() reads a different one per platform:
 * USERPROFILE on Windows and HOME everywhere else. Setting only HOME leaves
 * Windows detecting against the real user profile, where the answer depends on
 * what the runner happens to have, which passed on two of the three platforms
 * in the matrix and failed on the third.
 */
async function withHome<T>(markers: string[], body: () => Promise<T>): Promise<T> {
  const home = mkdtempSync(join(tmpdir(), "noetive-home-"));
  for (const marker of markers) mkdirSync(join(home, marker), { recursive: true });

  // CLAUDE_CONFIG_DIR relocates Claude Code's config out of the home directory,
  // so a developer shell that sets it would detect against their real config.
  const previous = { HOME: process.env.HOME, USERPROFILE: process.env.USERPROFILE, CLAUDE_CONFIG_DIR: process.env.CLAUDE_CONFIG_DIR };
  process.env.HOME = home;
  process.env.USERPROFILE = home;
  delete process.env.CLAUDE_CONFIG_DIR;

  // Asserted rather than assumed: if a future platform reads a third variable,
  // every detection test below starts answering from the real machine and fails
  // somewhere far from the cause. This names the cause.
  assert.equal(homedir(), home, "the scratch home did not take effect on this platform");

  try {
    return await body();
  } finally {
    for (const [name, value] of Object.entries(previous)) {
      if (value === undefined) delete process.env[name];
      else process.env[name] = value;
    }
  }
}

// Both spellings are in circulation: the published commands use one, and
// people type the other. Silently dropping either would look like the flag was
// accepted while the value never arrived.
test("flag values are accepted separated or joined by =", () => {
  assert.equal(parse(["init", "--client", "cursor"]).flags.get("client"), "cursor");
  assert.equal(parse(["init", "--client=cursor"]).flags.get("client"), "cursor");
  assert.equal(parse(["init", "-client", "cursor"]).flags.get("client"), "cursor");
});

// A silently-dropped --scope writes to the wrong file and reports success, so
// an unrecognised flag has to stop the command rather than be ignored.
test("an unknown flag is refused rather than ignored", () => {
  assert.throws(() => parse(["init", "--clint", "cursor"]), /unknown option/);
});

// A flag whose value is missing would otherwise consume the next flag as its
// value, configuring something nobody asked for.
test("a flag with no value is refused", () => {
  assert.throws(() => parse(["init", "--client"]), /needs a value/);
});

test("boolean flags take no value", () => {
  const parsed = parse(["init", "--dry-run", "--client", "cursor"]);

  assert.equal(parsed.flags.get("dry-run"), true);
  assert.equal(parsed.flags.get("client"), "cursor");
});

// The first bare word is the command; later ones are not. Treating a stray
// argument as a second command would silently run something else.
test("only the first bare word is the command", () => {
  assert.equal(parse(["init", "cursor"]).command, "init");
  assert.equal(parse([]).command, "help");
  assert.equal(parse(["--help"]).command, "help");
});

// A mistyped command must fail loudly and show what is available, not fall
// through to a default that does something unexpected.
test("an unknown command exits non-zero and lists the real ones", async () => {
  const { code, err } = await capture(["instal"]);

  assert.equal(code, 2);
  assert.match(err, /unknown command/);
  assert.match(err, /init/);
});

// Help is the recovery path when nothing else worked, so it has to name every
// command and every supported editor.
test("help names each command and each supported editor", async () => {
  const { code, out } = await capture(["help"]);

  assert.equal(code, 0);
  for (const command of ["init", "remove", "list", "doctor"]) {
    assert.match(out, new RegExp(command));
  }
  for (const client of clientIds()) {
    assert.match(out, new RegExp(client));
  }
});

// Requiring --client means a first-time user has to learn our client ids
// before they can install anything, and the ids are ours rather than theirs.
// One installed editor is an unambiguous answer, so it is the answer.
test("init without a client configures the one editor that is installed", async () => {
  const { code, out } = await withHome([".cursor"], () => capture(["init", "--yes", "--dry-run"]));

  assert.equal(code, 0);
  assert.match(out, /\.cursor/, `detection did not pick Cursor: ${out}`);
});

// Choosing between several would write into an editor the user did not name.
// Listing what was found is the useful failure; picking one is a silent one.
test("init without a client lists the choices when several are installed", async () => {
  const { code, err } = await withHome([".cursor", ".kiro"], () => capture(["init"]));

  assert.equal(code, 1);
  assert.match(err, /cursor/);
  assert.match(err, /kiro/);
});

// Nothing detected is not the same as nothing supported, so the message has to
// name the ids rather than only report the absence.
test("init without a client names the supported ids when none is installed", async () => {
  const { code, err } = await withHome([], () => capture(["init"]));

  assert.equal(code, 1);
  assert.match(err, /no supported editor/);
  assert.match(err, /cursor/);
});

// A typo in the client name is the most likely way to reach this, and the list
// of real names is more useful than the rejection alone.
test("an unknown client is refused and lists the supported ones", async () => {
  const { code, err } = await capture(["init", "--client", "vscode"]);

  assert.equal(code, 1);
  assert.match(err, /unknown client/);
  assert.match(err, /copilot/);
});

// `list` is how a user finds out what is configured before changing anything,
// so it must report every supported editor rather than only configured ones.
test("list reports every supported editor", async () => {
  const { code, out } = await capture(["list"]);

  assert.equal(code, 0);

  // Derived from the manifest, not listed here. A hand-written list is a list
  // somebody forgets to extend, and this test then keeps passing while quietly
  // covering one editor fewer than it claims to.
  for (const id of clientIds()) {
    assert.match(out, new RegExp(clientSpec(id).displayName.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  }
});

// An editor nothing can answer for must not be reported as unconfigured.
// Hermes has no command that says whether one server is present, so calling it
// "not configured" would fail `doctor` on a working install and prescribe the
// command the user has already run, every run, forever.
test("an editor whose state cannot be determined says so rather than guessing", async () => {
  const { out } = await withHome([".hermes"], () => capture(["list"]));

  assert.match(out, /Hermes[^\n]*cannot tell/, `expected Hermes to report an unknown state:\n${out}`);
  assert.doesNotMatch(out, /Hermes[^\n]*not configured/);
});

test("an undeterminable editor does not make doctor fail or prescribe a rerun", async () => {
  const { out } = await withHome([".hermes"], () => capture(["doctor"]));

  assert.doesNotMatch(out, /no editor is configured/, `doctor blamed an editor it cannot read:\n${out}`);
  assert.match(out, /cannot be checked from here/);
});

// The JSON output is what a script or an agent reads. It has to parse, and it
// has to be the only thing on stdout.
test("--json emits parseable output and nothing else", async () => {
  const { code, out } = await capture(["list", "--json"]);

  assert.equal(code, 0);
  const parsed = JSON.parse(out);
  assert.ok(Array.isArray(parsed), "expected an array of editors");
  assert.equal(parsed.length, clientIds().length);
});

// doctor's exit code is what a script keys off. A missing binary is a genuine
// fault and must be non-zero.
test("doctor exits non-zero when the binary cannot be found", async () => {
  // Everything else healthy, so the binary is the only thing that can fail.
  await withHome([".cursor"], () => withoutEditorCLIs(() => inWorkspace(async () => {
    await capture(["init", "--yes", "--client", "cursor"]);
    const { code, out } = await withBinary(undefined, () => withKey("keya_present", () => capture(["doctor"])));

    assert.equal(code, 1, "a missing binary did not fail the check");
    assert.match(out, /FAIL\s+binary/);
  })));
});

// The exit code is what a script keys off. A healthy install has to exit zero,
// or every script that runs doctor treats a working setup as broken.
test("doctor exits zero when every check passes", async () => {
  await withHome([".cursor"], () => withoutEditorCLIs(() => inWorkspace(async () => {
    await capture(["init", "--yes", "--client", "cursor"]);
    const { code, out } = await withBinary("/bin/noetive-mcp", () => withKey("keya_present", () => capture(["doctor"])));

    assert.doesNotMatch(out, /FAIL/);
    assert.equal(code, 0, out);
  })));
});

/**
 * withBinary runs body with doctor's binary lookup answering path, or failing
 * for undefined, so the result does not depend on what a local build left in
 * the tree.
 */
async function withBinary<T>(path: string | undefined, body: () => Promise<T>): Promise<T> {
  useBinaryResolver(() => {
    if (path === undefined) throw new Error("could not find the noetive-mcp binary (test)");
    return path;
  });
  try {
    return await body();
  } finally {
    useBinaryResolver(() => resolveBinary());
  }
}

test("doctor --json is parseable", async () => {
  const { out } = await capture(["doctor", "--json"]);

  const checks = JSON.parse(out);
  assert.ok(Array.isArray(checks));
  assert.ok(checks.some((c: { name: string }) => c.name === "binary"));
});

// A dry run is the safe way to see what init would do. It must write nothing
// and must say so, or the safety it advertises is not real.
test("a dry run reports the change and writes nothing", async () => {
  const workspace = mkdtempSync(join(tmpdir(), "noetive-cli-"));
  const cwd = process.cwd();
  process.chdir(workspace);
  try {
    const { code, out } = await capture(["init", "--yes", "--client", "cursor", "--scope", "project", "--dry-run"]);

    assert.equal(code, 0);
    assert.match(out, /Dry run: nothing was written/);
  } finally {
    process.chdir(cwd);
  }
});

// Removing something that was never configured is not an error: it is the
// answer to "is it gone", and reporting it as a failure would make an
// idempotent cleanup script fail.
test("removing an unconfigured editor succeeds and says nothing changed", async () => {
  const workspace = mkdtempSync(join(tmpdir(), "noetive-cli-"));
  const cwd = process.cwd();
  process.chdir(workspace);
  try {
    const { code, out } = await capture(["remove", "--client", "cursor", "--scope", "project"]);

    assert.equal(code, 0);
    assert.match(out, /was not configured/);
  } finally {
    process.chdir(cwd);
  }
});

// init and add are the same operation. A user who reaches for either has to get
// the same result.
test("add is an alias for init", async () => {
  const workspace = mkdtempSync(join(tmpdir(), "noetive-cli-"));
  const cwd = process.cwd();
  process.chdir(workspace);
  try {
    const { code, out } = await capture(["add", "--yes", "--client", "cursor", "--scope", "project", "--dry-run"]);

    assert.equal(code, 0);
    assert.match(out, /noetive/);
  } finally {
    process.chdir(cwd);
  }
});


/**
 * withoutEditorCLIs runs body with PATH emptied, so an editor's own CLI is
 * never invoked.
 *
 * Delegating to a real `claude` on the runner's PATH would make these tests
 * depend on which version of it happens to be installed, and would write into
 * that machine's own configuration. Emptying PATH takes the documented
 * file-merge fallback instead, which is the path with something to assert
 * about.
 */
async function withoutEditorCLIs<T>(body: () => Promise<T>): Promise<T> {
  const previous = process.env.PATH;
  process.env.PATH = mkdtempSync(join(tmpdir(), "noetive-nopath-"));
  try {
    return await body();
  } finally {
    process.env.PATH = previous;
  }
}

/** inWorkspace runs body from a scratch project directory. */
async function inWorkspace<T>(body: (dir: string) => Promise<T>): Promise<T> {
  const dir = mkdtempSync(join(tmpdir(), "noetive-cli-"));
  const cwd = process.cwd();
  process.chdir(dir);
  try {
    return await body(dir);
  } finally {
    process.chdir(cwd);
  }
}

// The flags exist so a scripted install never has to answer a question. An
// unknown flag is an error rather than being ignored, so each of these has to
// be declared or the documented command line stops working.
test("every documented init flag is accepted", () => {
  const flags = parse([
    "init", "--client", "cursor", "--namespace", "acme", "--model", "m", "--dimensions", "1024",
    "--disable-global-ns", "--skills", "semql,doctor", "--yes",
  ]).flags;

  assert.equal(flags.get("namespace"), "acme");
  assert.equal(flags.get("dimensions"), "1024");
  assert.equal(flags.get("skills"), "semql,doctor");
  assert.equal(flags.get("disable-global-ns"), true);
  assert.equal(flags.get("yes"), true);
});

// Two flags that answer the same question in opposite directions. Resolving one
// of them silently would be the opposite of what half the readers of that
// command line expect.
test("asking to both close and open the shared namespace is refused", async () => {
  await inWorkspace(async () => {
    const { code, err } = await capture([
      "init", "--client", "cursor", "--scope", "project", "--disable-global-ns", "--allow-global-ns",
    ]);

    assert.equal(code, 1);
    assert.match(err, /opposite things/);
  });
});

// The decision is written whichever way it went. Omitting the variable when
// someone said "leave it open" would let an exported variable elsewhere close
// it behind their back.
test("both answers about the shared namespace reach the config", async () => {
  for (const [flag, expected] of [["--disable-global-ns", "1"], ["--allow-global-ns", "0"]] as const) {
    await inWorkspace(async (dir) => {
      const { code } = await capture(["init", "--yes", "--client", "cursor", "--scope", "project", "--namespace", "acme", flag]);
      assert.equal(code, 0);

      const config = JSON.parse(readFileSync(join(dir, ".cursor", "mcp.json"), "utf8"));
      assert.equal(config.mcpServers.noetive.env.NOETIVE_DISABLE_GLOBAL_NS, expected);
    });
  }
});

// A typo in --skills would otherwise report a successful install of a skill
// that was never written.
test("a skill nobody ships is refused by name", async () => {
  await withoutEditorCLIs(async () => await inWorkspace(async () => {
    const { code, err } = await capture([
      "init", "--client", "claude-code", "--scope", "project", "--skills", "semql,nonsense",
    ]);

    assert.equal(code, 1);
    assert.match(err, /nonsense/);
    // The refusal is only useful if it says what would have worked.
    assert.match(err, /available: [^\n]*semql/);
  }));
});

// Naming skills one by one is the third way to answer --skills. It must install
// exactly those, not refuse a name that ships and not add ones nobody named.
test("a named list of skills installs exactly those", async () => {
  await withoutEditorCLIs(async () => await inWorkspace(async (dir) => {
    const { code, err } = await capture([
      "init", "--yes", "--client", "claude-code", "--scope", "project", "--skills", "semql",
    ]);

    assert.equal(code, 0, err);
    assert.ok(existsSync(join(dir, ".claude", "skills", "semql", "SKILL.md")));
    assert.equal(existsSync(join(dir, ".claude", "skills", "doctor")), false);
  }));
});

// A rerun is the normal way to change one setting. Reporting "Installed 0
// skill files" there reads as a failure to install, and "up to date" after
// --skills none reads as skills being present that are not.
test("the skills report says what happened: installed, already current, or nothing", async () => {
  await withoutEditorCLIs(async () => await inWorkspace(async () => {
    const argv = ["init", "--yes", "--client", "claude-code", "--scope", "project", "--skills", "all"];
    await capture(argv);
    const again = await capture(argv);
    const none = await capture(["init", "--yes", "--client", "claude-code", "--scope", "project", "--skills", "none"]);

    assert.match(again.out, /already up to date/);
    assert.doesNotMatch(again.out, /Installed \d+ skill/);
    assert.doesNotMatch(none.out, /skill/i);
  }));
});

// --skills none is how a scripted install says "configure the server only".
test("skills none installs the server and no skills", async () => {
  await withoutEditorCLIs(async () => await inWorkspace(async (dir) => {
    const { code } = await capture([
      "init", "--yes", "--client", "claude-code", "--scope", "project", "--skills", "none",
    ]);

    assert.equal(code, 0);
    assert.equal(existsSync(join(dir, ".claude", "skills")), false);
  }));
});

test("skills all installs every skill the package ships", async () => {
  await withoutEditorCLIs(async () => await inWorkspace(async (dir) => {
    const { code, out } = await capture([
      "init", "--yes", "--client", "claude-code", "--scope", "project", "--skills", "all",
    ]);

    assert.equal(code, 0);
    assert.match(out, /Installed \d+ skill file/);
    assert.ok(existsSync(join(dir, ".claude", "skills", "semql", "SKILL.md")));
    assert.ok(existsSync(join(dir, ".claude", "skills", "semql", "references")));
  }));
});

// remove is documented as reversing what init did. Skills that came in with the
// entry go out with it, or the editor keeps loading instructions for tools it
// no longer has.
test("remove takes the installed skills with it", async () => {
  await withoutEditorCLIs(async () => await inWorkspace(async (dir) => {
    await capture(["init", "--yes", "--client", "claude-code", "--scope", "project", "--skills", "all"]);
    const { code } = await capture(["remove", "--client", "claude-code", "--scope", "project"]);

    assert.equal(code, 0);
    assert.equal(existsSync(join(dir, ".claude", "skills", "semql")), false);
  }));
});

// An editor with nowhere to put skills is told so, rather than being given
// files in a shape it silently ignores.
test("an editor with no skills directory says so", async () => {
  await inWorkspace(async () => {
    const { out } = await capture(["init", "--yes", "--client", "cursor", "--scope", "project", "--skills", "all"]);

    assert.match(out, /no skills directory/);
  });
});

// --dry-run is what a careful person runs to see what would happen before it
// happens. Printing the key there puts it in their scrollback, their shell
// history and any screen share that is running.
test("a dry run does not print the API key it would write", async () => {
  await inWorkspace(async () => {
    const { out } = await capture([
      "init", "--yes", "--client", "cursor", "--scope", "project", "--api-key", "keya_notInTheOutput", "--dry-run",
    ]);

    assert.equal(out.includes("keya_notInTheOutput"), false, "the key reached the terminal");
    assert.match(out, /<your key>/);
  });
});

// The variable reference is not a secret, and hiding it would leave a dry run
// unable to show the one thing it is meant to show.
test("the variable reference survives a dry run intact", async () => {
  await inWorkspace(async () => {
    const { out } = await capture(["init", "--yes", "--client", "cursor", "--scope", "project", "--dry-run"]);

    assert.match(out, /\$\{NOETIVE_KEY_SECRET\}/);
  });
});

// '$NOETIVE_KEY_SECRET' single-quoted reaches us unexpanded. Written into the
// config it is sent to Noetive as the key and comes back "unauthorized", which
// sends the user to check an account that is fine.
test("a variable reference passed as --api-key is refused", async () => {
  await inWorkspace(async (dir) => {
    const { code, err } = await capture([
      "init", "--yes", "--client", "kiro", "--scope", "workspace", "--api-key", "$NOETIVE_KEY_SECRET",
    ]);

    assert.equal(code, 1);
    assert.match(err, /variable reference/);
    assert.equal(existsSync(join(dir, ".kiro")), false);
  });
});

// Several editors installed is genuinely ambiguous. Without a terminal it stays
// a refusal listing the candidates, because picking writes into an editor the
// user did not mean to configure.
test("ambiguous detection with no terminal refuses and names the candidates", async () => {
  await withHome([".cursor", ".kiro"], async () => {
    await inWorkspace(async () => {
      const { code, err } = await capture(["init"]);

      assert.equal(code, 1);
      assert.match(err, /several editors/);
      assert.match(err, /cursor/);
    });
  });
});

/** withKey runs body with NOETIVE_KEY_SECRET set to value, or unset for undefined. */
async function withKey<T>(value: string | undefined, body: () => Promise<T>): Promise<T> {
  const previous = process.env.NOETIVE_KEY_SECRET;
  if (value === undefined) delete process.env.NOETIVE_KEY_SECRET;
  else process.env.NOETIVE_KEY_SECRET = value;
  try {
    return await body();
  } finally {
    if (previous === undefined) delete process.env.NOETIVE_KEY_SECRET;
    else process.env.NOETIVE_KEY_SECRET = previous;
  }
}

interface DoctorCheck {
  readonly name: string;
  readonly status: string;
  readonly detail: string;
}

/** doctorChecks runs doctor --json and returns its exit code and checks. */
async function doctorChecks(): Promise<{ code: number; checks: DoctorCheck[] }> {
  const { code, out } = await capture(["doctor", "--json"]);
  return { code, checks: JSON.parse(out) as DoctorCheck[] };
}

// `--api-key "$NOETIVE_KEY_SECRET"` with the variable unset expands to an empty
// argument. Accepted, it is the "write no key" answer and the install succeeds
// with every later call refused. `--api-key=` is how to mean empty on purpose,
// and it must keep working: it is how a Kiro or Codex user says "no key here".
test("an empty separate value is refused, and --flag= still means empty on purpose", async () => {
  await inWorkspace(async (dir) => {
    const unset = await capture(["init", "--yes", "--client", "kiro", "--scope", "workspace", "--api-key", ""]);
    assert.equal(unset.code, 2);
    assert.match(unset.err, /not set in this shell/);
    assert.match(unset.err, /--api-key=/);
    assert.equal(existsSync(join(dir, ".kiro")), false);

    const deliberate = await capture(["init", "--yes", "--client", "kiro", "--scope", "workspace", "--api-key="]);
    assert.equal(deliberate.code, 0, deliberate.err);
  });
});

// An editor nobody configured is either one nothing can read (Hermes) or simply
// not configured. Calling every unconfigured editor "cannot tell" would hide
// the one fix that applies, running init.
test("list says 'cannot tell' only for an editor nothing can read", async () => {
  const rows = await withHome([".cursor", ".hermes"], async () =>
    JSON.parse((await capture(["list", "--json"])).out) as { client: string; undetermined: boolean }[]);

  assert.equal(rows.find((row) => row.client === "hermes")?.undetermined, true);
  assert.equal(rows.find((row) => row.client === "cursor")?.undetermined, false);
});

// An installed, unconfigured editor gets the command that configures it. Telling
// a Cursor user their editor "cannot be checked" sends them nowhere.
test("doctor prescribes init for an installed editor that is not configured", async () => {
  const { checks } = await withHome([".cursor"], () => withKey(undefined, doctorChecks));

  const cursor = checks.find((check) => check.name === clientSpec("cursor").displayName);
  assert.match(cursor?.detail ?? "", /not configured; run: .*init --client cursor/);
  assert.doesNotMatch(cursor?.detail ?? "", /cannot be checked/);
});

// No editor installed is not "no editor configured": there is nothing to
// configure yet. And with nothing configured, a missing key is a real fault.
// Every check failing here also pins the exit code to the failures, not to
// the passes.
test("doctor on a machine with no editors fails on the key, not on the editors", async () => {
  const { code, checks } = await withHome([], () => withKey(undefined, doctorChecks));

  assert.equal(checks.some((check) => check.name === "editors"), false, "blamed editors on a machine that has none");
  assert.equal(checks.find((check) => check.name === "api key")?.status, "fail");
  assert.equal(code, 1);
});

// An editor configured with --api-key carries its own key, so a shell without
// one is worth mentioning, not a fault. Failing here makes doctor red on a
// working install.
test("doctor treats a missing shell key as information once an editor is configured", async () => {
  await withHome([".cursor"], () => withoutEditorCLIs(() => inWorkspace(async () => {
    await capture(["init", "--yes", "--client", "cursor", "--api-key", "keya_configured"]);
    const { checks } = await withKey(undefined, doctorChecks);

    assert.equal(checks.find((check) => check.name === "api key")?.status, "info");
  })));
});

// Windows writes an unexpanded variable as %NAME%. Exported as the "key", it
// is sent to Noetive verbatim and comes back unauthorized, so doctor has to
// name it for what it is.
test("doctor fails a shell key that is an unexpanded variable in either syntax", async () => {
  for (const placeholder of ["${NOETIVE_KEY_SECRET}", "%NOETIVE_KEY_SECRET%"]) {
    const { checks } = await withHome([], () => withKey(placeholder, doctorChecks));
    const key = checks.find((check) => check.name === "api key");

    assert.equal(key?.status, "fail", `${placeholder} was accepted as a key`);
    assert.match(key?.detail ?? "", /literal text/);
  }
});

/** envOf reads the noetive entry's env block from a project-scoped Cursor config. */
function envOf(dir: string): Record<string, string> {
  return JSON.parse(readFileSync(join(dir, ".cursor", "mcp.json"), "utf8")).mcpServers.noetive.env;
}

// A first install from an agent's shell has nobody to answer questions. It must
// still produce a server whose first call works, pointed at the shared
// namespace with the model and dimensions that namespace is provisioned with.
test("an install with no terminal and no answers uses the testing defaults", async () => {
  await inWorkspace(async (dir) => {
    const { code, err } = await capture(["init", "--client", "cursor", "--scope", "project"]);

    assert.equal(code, 0, err);
    const env = envOf(dir);
    assert.equal(env.NOETIVE_NAMESPACE, "global");
    assert.equal(env.NOETIVE_MODEL, "Qwen3-Embedding-4B");
    assert.equal(env.NOETIVE_DIMENSIONS, "1024");
    // Open, because closing the namespace the install routes to refuses every call.
    assert.equal(env.NOETIVE_DISABLE_GLOBAL_NS, "0");
  });
});

// The shared namespace is cross-tenant. A default the user does not notice
// means their agents publish where other Noetive users can read, so the
// install has to say so, and say how to move off it.
test("a defaulted shared namespace is flagged with how to change it", async () => {
  await inWorkspace(async () => {
    const { out } = await capture(["init", "--client", "cursor", "--scope", "project"]);

    assert.match(out, /WARNING: namespace is "global"/);
    assert.match(out, /shared by every Noetive user/);
    assert.match(out, /--namespace <name> --disable-global-ns/);
  });
});

// A namespace the user chose is not a default and earns no warning; what was
// still filled in is listed, so nothing changes behind their back.
test("a named namespace is not flagged, and what was filled in around it is listed", async () => {
  await inWorkspace(async (dir) => {
    const { out } = await capture(["init", "--client", "cursor", "--scope", "project", "--namespace", "acme"]);

    assert.doesNotMatch(out, /WARNING/);
    assert.match(out, /model\s+Qwen3-Embedding-4B/);
    // Once they have their own namespace, the shared one is only a place for
    // an unsure agent to leak into, so it is closed unless they say otherwise.
    assert.equal(envOf(dir).NOETIVE_DISABLE_GLOBAL_NS, "1");
  });
});

// An install that answered everything has nothing to report as defaulted, and
// listing defaults anyway would teach people to skip the list.
test("an install with every answer given lists no defaults", async () => {
  await inWorkspace(async () => {
    const { code, out } = await capture([
      "init", "--client", "cursor", "--scope", "project",
      "--namespace", "acme", "--model", "m", "--dimensions", "8", "--allow-global-ns",
    ]);

    assert.equal(code, 0);
    assert.doesNotMatch(out, /Not asked|WARNING/);
  });
});

// The warning's advice is the rerun. Following it must replace the defaults,
// not merge with them, or the user ends up in their namespace with the shared
// one still open.
test("rerunning with the suggested change moves the install off the shared namespace", async () => {
  await inWorkspace(async (dir) => {
    await capture(["init", "--client", "cursor", "--scope", "project"]);
    await capture(["init", "--client", "cursor", "--scope", "project", "--namespace", "acme", "--disable-global-ns"]);

    assert.equal(envOf(dir).NOETIVE_NAMESPACE, "acme");
    assert.equal(envOf(dir).NOETIVE_DISABLE_GLOBAL_NS, "1");
  });
});

// "name one on every call" is a legitimate answer. Model and dimensions then
// describe nothing, so none are written, and the shared namespace is closed
// because nothing routes there.
test("an empty namespace gets no model or dimensions", async () => {
  await inWorkspace(async (dir) => {
    await capture(["init", "--client", "cursor", "--scope", "project", "--namespace="]);

    const env = envOf(dir);
    assert.equal("NOETIVE_NAMESPACE" in env, false);
    assert.equal("NOETIVE_MODEL" in env, false);
    assert.equal("NOETIVE_DIMENSIONS" in env, false);
    assert.equal(env.NOETIVE_DISABLE_GLOBAL_NS, "1", "nothing routes to the shared namespace, so it should be closed");
  });
});

// Routing to a namespace and closing it on the same command line produces a
// server that refuses every call. Names are case-insensitive, so the check is.
test("naming the shared namespace and closing it is refused", async () => {
  await inWorkspace(async (dir) => {
    const { code, err } = await capture(["init", "--client", "cursor", "--scope", "project", "--namespace", "Global", "--disable-global-ns"]);

    assert.equal(code, 1);
    assert.match(err, /every call would be refused/);
    assert.equal(existsSync(join(dir, ".cursor")), false);
  });
});

// An agent reads --json, not prose. The defaults have to be there, each with a
// change made of flags the command line accepts, or following it is refused.
test("--json lists each default with a change the command line accepts", async () => {
  await inWorkspace(async () => {
    const { code, out } = await capture(["init", "--client", "claude-code", "--scope", "project", "--json"]);

    assert.equal(code, 0);
    const { defaulted } = JSON.parse(out) as { defaulted: { setting: string; value: string; change: string; warning?: string }[] };
    assert.ok(defaulted.find((d) => d.setting === "namespace")?.warning, "the shared namespace default carried no warning");
    assert.ok(defaulted.some((d) => d.setting === "skills"), "claude-code has a skills directory, so skills were defaulted");
    for (const item of defaulted) {
      const argv = ["init", ...item.change.split(" ").map((word) => (word.startsWith("<") ? "x" : word))];
      assert.doesNotThrow(() => parse(argv), `the change "${item.change}" is not a valid command line`);
    }
  });
});

// Every editor the installer can write without its own CLI must come out of a
// bare first install configured, or the README prompt fails for that editor.
// Derived from the manifest so a new editor is covered.
test("a bare first install configures every editor", async () => {
  const merged = clientIds().filter((id) => clientSpec(id).cli?.fallback !== "none");
  assert.ok(merged.length >= 4, "fewer editors under test than expected; the filter is wrong");

  for (const id of merged) {
    await withHome([], () => withoutEditorCLIs(() => inWorkspace(async () => {
      const { code, err } = await capture(["init", "--client", id]);
      assert.equal(code, 0, `${id}: ${err}`);

      const rows = JSON.parse((await capture(["list", "--json"])).out) as { client: string; configured: boolean }[];
      assert.equal(rows.find((row) => row.client === id)?.configured, true, `${id}: reported success but is not configured`);
    })));
  }
});

// Some agents run commands inside a real terminal. There, without --no-input,
// init would draw a prompt nobody answers and the agent would hang.
test("--no-input asks nothing even in a terminal, and uses the defaults", async () => {
  const tty = { stdin: process.stdin.isTTY, stdout: process.stdout.isTTY };
  process.stdin.isTTY = true;
  process.stdout.isTTY = true;
  usePrompter(() => {
    throw new Error("prompted despite --no-input");
  });
  try {
    await inWorkspace(async (dir) => {
      const { code, err } = await capture(["init", "--no-input", "--client", "cursor", "--scope", "project"]);

      assert.equal(code, 0, err);
      assert.equal(envOf(dir).NOETIVE_NAMESPACE, "global");
    });
  } finally {
    process.stdin.isTTY = tty.stdin;
    process.stdout.isTTY = tty.stdout;
    usePrompter(() => terminalPrompter());
  }
});

// An editor installed and none configured is the one state where nothing can
// reach Noetive, so it is the one doctor must fail on. Cursor's state can be
// read, so it neither counts as configured nor holds the failure back the way
// an editor nothing can read does.
test("doctor fails when an editor is installed and none is configured", async () => {
  const { code, checks } = await withHome([".cursor"], () => withBinary("/bin/noetive-mcp", () => withKey("keya_present", doctorChecks)));

  assert.equal(checks.find((check) => check.name === "editors")?.status, "fail");
  assert.equal(code, 1);
});

// "Close the shared namespace" is an instruction an agent will pass on its
// own. With no namespace named, the install would default to the very
// namespace it closes and refuse every call, so the pair is refused with the
// flag that fixes it.
test("closing the shared namespace without naming another is refused", async () => {
  await inWorkspace(async (dir) => {
    const { code, err } = await capture(["init", "--client", "cursor", "--scope", "project", "--disable-global-ns"]);

    assert.equal(code, 1);
    assert.match(err, /every call would be refused/);
    assert.match(err, /--namespace <name>/);
    assert.equal(existsSync(join(dir, ".cursor")), false);
  });
});

// The shared namespace is provisioned with one model at one size. A model
// passed without a namespace lands there and fails every call at the server,
// which reports it far from the install that caused it.
test("a model the shared namespace does not use is refused there", async () => {
  await inWorkspace(async (dir) => {
    const { code, err } = await capture(["init", "--client", "cursor", "--scope", "project", "--model", "other-model"]);

    assert.equal(code, 1);
    assert.match(err, /provisioned with Qwen3-Embedding-4B at 1024/);
    assert.equal(existsSync(join(dir, ".cursor")), false);
  });
});

// Namespace names are case-insensitive, so GLOBAL is the shared namespace and
// gets the shared namespace's treatment: left open, and warned about only if
// defaulted, which it was not.
test("the shared namespace is recognised in any case", async () => {
  await inWorkspace(async (dir) => {
    const { code, err } = await capture(["init", "--client", "cursor", "--scope", "project", "--namespace", "GLOBAL"]);

    assert.equal(code, 0, err);
    assert.equal(envOf(dir).NOETIVE_DISABLE_GLOBAL_NS, "0");
  });
});

// Keeping the shared namespace open beside your own is a legitimate choice.
// The default for a named namespace is to close it, so the flag has to win.
test("--allow-global-ns keeps the shared namespace open beside a named one", async () => {
  await inWorkspace(async (dir) => {
    await capture(["init", "--client", "cursor", "--scope", "project", "--namespace", "acme", "--allow-global-ns"]);

    assert.equal(envOf(dir).NOETIVE_DISABLE_GLOBAL_NS, "0");
  });
});

// --yes promises nobody answers, the same as --no-input. If only --no-input
// reached the adapters, `init --yes --client hermes` run by an agent in a
// pseudo-terminal would hand it Hermes' tool picker and hang.
//
// The stand-in for `hermes` is this Node binary under that name: it answers
// --version on every platform, where a shell script is not an executable
// Windows can start without a shell, and the installer then refuses for a
// missing CLI instead, which is not the refusal under test. Had `mcp add` run,
// Node would have failed to load a script called "mcp" and the refusal below
// would be a different message.
test("--yes refuses an editor CLI that would prompt, even in a terminal", async () => {
  const bin = mkdtempSync(join(tmpdir(), "noetive-hermes-"));
  if (process.platform === "win32") copyFileSync(process.execPath, join(bin, "hermes.exe"));
  else symlinkSync(process.execPath, join(bin, "hermes"));
  const tty = { stdin: process.stdin.isTTY, stdout: process.stdout.isTTY };
  const path = process.env.PATH;
  process.stdin.isTTY = true;
  process.stdout.isTTY = true;
  process.env.PATH = bin;
  try {
    const { code, err } = await withHome([".hermes"], () => capture(["init", "--yes", "--client", "hermes"]));

    assert.equal(code, 1);
    assert.match(err, /told to ask nothing/);
  } finally {
    process.stdin.isTTY = tty.stdin;
    process.stdout.isTTY = tty.stdout;
    process.env.PATH = path;
  }
});

// The prompt checks dimensions; a flag skipping that check wrote "abc" into the
// config and exited 0, and the server then would not start, reported only into
// the editor's log.
test("--dimensions that is not a whole number in range is refused before writing", async () => {
  for (const value of ["abc", "0", "65536", "1.5"]) {
    await inWorkspace(async (dir) => {
      const { code, err } = await capture([
        "init", "--no-input", "--client", "cursor", "--scope", "project", "--namespace", "mine", "--dimensions", value,
      ]);

      assert.equal(code, 1, value);
      assert.match(err, /--dimensions: A whole number/, value);
      assert.equal(existsSync(join(dir, ".cursor")), false, value);
    });
  }
});

// Kiro and Codex need the key written into their config. Passing it as
// --api-key "$NOETIVE_KEY_SECRET" puts it in the process list and in the
// command log of the agent that ran it; --api-key-from-env writes the same key
// with only the flag's name on the command line.
test("--api-key-from-env writes the exported key without it being on the command line", async () => {
  await withKey("keya_fromTheEnvironment", () =>
    inWorkspace(async (dir) => {
      const argv = ["init", "--no-input", "--client", "cursor", "--scope", "project", "--api-key-from-env"];
      const { code, out, err } = await capture(argv);

      assert.equal(code, 0, err);
      assert.equal(envOf(dir).NOETIVE_KEY_SECRET, "keya_fromTheEnvironment");
      assert.equal(out.includes("keya_fromTheEnvironment"), false, "the key reached the terminal");
    }),
  );
});

// Asking for the variable while it is unset would otherwise install a server
// that reports success and refuses every call.
test("--api-key-from-env with the variable unset, empty or unexpanded is refused before writing", async () => {
  for (const value of [undefined, "", "  ", "$NOETIVE_KEY_SECRET"]) {
    await withKey(value, () =>
      inWorkspace(async (dir) => {
        const { code, err } = await capture(["init", "--no-input", "--client", "cursor", "--scope", "project", "--api-key-from-env"]);

        assert.equal(code, 1, String(value));
        assert.match(err, /--api-key-from-env/, String(value));
        assert.equal(existsSync(join(dir, ".cursor")), false, String(value));
      }),
    );
  }
});

// Two sources for one key is a mistake whichever was meant; picking one
// silently writes a key the user did not choose.
test("--api-key with --api-key-from-env is refused", async () => {
  await withKey("keya_fromTheEnvironment", () =>
    inWorkspace(async () => {
      const { code, err } = await capture([
        "init", "--no-input", "--client", "cursor", "--scope", "project", "--api-key-from-env", "--api-key", "keya_other",
      ]);

      assert.equal(code, 1);
      assert.match(err, /Pass one/);
    }),
  );
});

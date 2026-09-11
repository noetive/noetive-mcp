import assert from "node:assert/strict";
import { existsSync, mkdirSync, mkdtempSync, readFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { parse, run } from "../src/cli";
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
 * what the runner happens to have — which passed on two of the three platforms
 * in the matrix and failed on the third.
 */
async function withHome<T>(markers: string[], body: () => Promise<T>): Promise<T> {
  const home = mkdtempSync(join(tmpdir(), "noetive-home-"));
  for (const marker of markers) mkdirSync(join(home, marker), { recursive: true });

  const previous = { HOME: process.env.HOME, USERPROFILE: process.env.USERPROFILE };
  process.env.HOME = home;
  process.env.USERPROFILE = home;

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

// Both spellings are in circulation — the published commands use one, and
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
  const { code, out } = await withHome([".cursor"], () => capture(["init", "--dry-run"]));

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
// command the user has already run — every run, forever.
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
  const { code, out } = await capture(["doctor"]);

  assert.equal(code, 1, "a missing binary did not fail the check");
  assert.match(out, /binary/);
});

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
    const { code, out } = await capture(["init", "--client", "cursor", "--scope", "project", "--dry-run"]);

    assert.equal(code, 0);
    assert.match(out, /Dry run: nothing was written/);
  } finally {
    process.chdir(cwd);
  }
});

// Removing something that was never configured is not an error — it is the
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
    const { code, out } = await capture(["add", "--client", "cursor", "--scope", "project", "--dry-run"]);

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
      const { code } = await capture(["init", "--client", "cursor", "--scope", "project", flag]);
      assert.equal(code, 0);

      const config = JSON.parse(readFileSync(join(dir, ".cursor", "mcp.json"), "utf8"));
      assert.equal(config.mcpServers.noetive.env.NOETIVE_DISABLE_GLOBAL_NS, expected);
    });
  }
});

// Neither flag means neither answer, which is what every release before the
// interview wrote. An install nobody could be asked about must not acquire a
// setting nobody chose.
test("an unanswered shared-namespace question writes no variable", async () => {
  await inWorkspace(async (dir) => {
    await capture(["init", "--client", "cursor", "--scope", "project"]);

    const config = JSON.parse(readFileSync(join(dir, ".cursor", "mcp.json"), "utf8"));
    assert.equal("NOETIVE_DISABLE_GLOBAL_NS" in config.mcpServers.noetive.env, false);
  });
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
  }));
});

// --skills none is how a scripted install says "configure the server only".
test("skills none installs the server and no skills", async () => {
  await withoutEditorCLIs(async () => await inWorkspace(async (dir) => {
    const { code } = await capture([
      "init", "--client", "claude-code", "--scope", "project", "--skills", "none",
    ]);

    assert.equal(code, 0);
    assert.equal(existsSync(join(dir, ".claude", "skills")), false);
  }));
});

test("skills all installs every skill the package ships", async () => {
  await withoutEditorCLIs(async () => await inWorkspace(async (dir) => {
    const { code, out } = await capture([
      "init", "--client", "claude-code", "--scope", "project", "--skills", "all",
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
    await capture(["init", "--client", "claude-code", "--scope", "project", "--skills", "all"]);
    const { code } = await capture(["remove", "--client", "claude-code", "--scope", "project"]);

    assert.equal(code, 0);
    assert.equal(existsSync(join(dir, ".claude", "skills", "semql")), false);
  }));
});

// An editor with nowhere to put skills is told so, rather than being given
// files in a shape it silently ignores.
test("an editor with no skills directory says so", async () => {
  await inWorkspace(async () => {
    const { out } = await capture(["init", "--client", "cursor", "--scope", "project", "--skills", "all"]);

    assert.match(out, /no skills directory/);
  });
});

// --dry-run is what a careful person runs to see what would happen before it
// happens. Printing the key there puts it in their scrollback, their shell
// history and any screen share that is running.
test("a dry run does not print the API key it would write", async () => {
  await inWorkspace(async () => {
    const { out } = await capture([
      "init", "--client", "cursor", "--scope", "project", "--api-key", "keyu_notInTheOutput", "--dry-run",
    ]);

    assert.equal(out.includes("keyu_notInTheOutput"), false, "the key reached the terminal");
    assert.match(out, /<your key>/);
  });
});

// The variable reference is not a secret, and hiding it would leave a dry run
// unable to show the one thing it is meant to show.
test("the variable reference survives a dry run intact", async () => {
  await inWorkspace(async () => {
    const { out } = await capture(["init", "--client", "cursor", "--scope", "project", "--dry-run"]);

    assert.match(out, /\$\{NOETIVE_KEY_SECRET\}/);
  });
});

// This is load-bearing. postinstall, CI and editors all invoke init with no
// terminal, and a prompt in front of any of them is not a question but a
// process that never exits.
test("an install with no terminal asks nothing and still writes", async () => {
  await inWorkspace(async (dir) => {
    // The test runner has no TTY on stdin, which is the same condition CI and
    // an editor present. Anything that tried to prompt here would hang.
    const { code } = await capture(["init", "--client", "cursor", "--scope", "project", "--namespace", "acme"]);

    assert.equal(code, 0);
    const config = JSON.parse(readFileSync(join(dir, ".cursor", "mcp.json"), "utf8"));
    assert.equal(config.mcpServers.noetive.env.NOETIVE_NAMESPACE, "acme");
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

#!/usr/bin/env node
"use strict";

// Argv router for @noetive/mcp-server.
//
// With no subcommand — or with `serve` — this execs the Go binary, which speaks
// MCP over stdio. That default is load-bearing: the "Add to Kiro" deeplink on
// noetive.io launches `npx @noetive/mcp-server` with no arguments at all, and
// every editor config written by `init` spawns it the same way. Anything else
// here would break every advertised install path.
//
// Every other subcommand is configuration and is handled in JavaScript.

const { spawn } = require("node:child_process");

const CONFIG_COMMANDS = new Set(["init", "add", "remove", "list", "doctor", "help"]);

function main() {
  const argv = process.argv.slice(2);
  const first = argv[0];

  if (first !== undefined && CONFIG_COMMANDS.has(first)) {
    return runCli(argv);
  }
  if (first === "--help" || first === "-h") {
    return runCli(["help"]);
  }

  return serve(first === "serve" ? argv.slice(1) : argv);
}

function runCli(argv) {
  const { run } = require("../dist/src/cli.js");
  run(argv).then(
    (code) => process.exit(code),
    (err) => {
      console.error(`noetive-mcp: ${err && err.message ? err.message : err}`);
      process.exit(1);
    },
  );
}

function serve(argv) {
  let binary;
  try {
    binary = require("../dist/src/resolveBinary.js").resolveBinary();
  } catch (err) {
    console.error(`noetive-mcp: ${err && err.message ? err.message : err}`);
    process.exit(1);
    return;
  }

  // stdio is inherited rather than piped: the protocol runs over this process's
  // own stdin and stdout, and any relaying would add a buffer between the
  // editor and the server for no benefit.
  const child = spawn(binary, argv, { stdio: "inherit" });

  // Signals are forwarded so an editor closing the server actually stops it,
  // rather than leaving an orphan holding a subscription open.
  for (const signal of ["SIGINT", "SIGTERM", "SIGHUP"]) {
    process.on(signal, () => child.kill(signal));
  }

  child.on("error", (err) => {
    console.error(`noetive-mcp: could not start ${binary}: ${err.message}`);
    process.exit(1);
  });
  child.on("exit", (code, signal) => {
    process.exit(signal ? 1 : code === null ? 1 : code);
  });
}

main();

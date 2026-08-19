#!/usr/bin/env node
"use strict";

// Stamps one version across every artifact that carries one.
//
// The git tag is the single source of truth. The wrapper pins its platform
// dependencies to exactly its own version, so a mismatch would let a wrapper
// resolve a binary built from different source — the kind of skew that produces
// bug reports nobody can reproduce.
//
//     node scripts/stamp-version.js 1.4.0

const { readFileSync, writeFileSync } = require("node:fs");
const { join } = require("node:path");

const ROOT = join(__dirname, "..");

function main() {
  const version = process.argv[2];
  if (!version || !/^\d+\.\d+\.\d+(-[\w.]+)?$/.test(version)) {
    console.error("usage: stamp-version.js <semver>  (e.g. 1.4.0)");
    process.exit(2);
  }

  stampInstaller(version);
  stampJson(join(ROOT, "server.json"), (doc) => {
    doc.version = version;
    for (const pkg of doc.packages ?? []) pkg.version = version;
  });
  stampJson(join(ROOT, ".claude-plugin", "plugin.json"), (doc) => {
    doc.version = version;
  });

  console.log(`stamped ${version}`);
}

function stampInstaller(version) {
  const path = join(ROOT, "installer", "package.json");
  stampJson(path, (doc) => {
    doc.version = version;
    for (const name of Object.keys(doc.optionalDependencies ?? {})) {
      doc.optionalDependencies[name] = version;
    }
  });
}

function stampJson(path, mutate) {
  const doc = JSON.parse(readFileSync(path, "utf8"));
  mutate(doc);
  writeFileSync(path, `${JSON.stringify(doc, null, 2)}\n`);
}

main();

#!/usr/bin/env node
"use strict";

// Stamps one version across every file that carries one.
//
// Run this to bump the version, then `make emit` to push tools/manifest.yaml's
// new value into the three plugin manifests, and commit the result:
//
//     node scripts/stamp-version.js 1.4.0
//     make emit
//
// The committed tree is what ships. Claude Code and Kiro serve their manifests
// out of the repository, the wrapper pins its platform dependencies to exactly
// its own version, and server.json is handed to the MCP registry verbatim — so
// a file left stale here is a published artifact describing a different build.
// The release workflow refuses a tag that disagrees with tools/manifest.yaml,
// and packaging/emit's tests refuse a tree whose version files disagree with
// each other; between them the tag, the tree and the artifacts cannot drift.
//
// The plugin manifests are deliberately absent from the list below: they are
// generated, so stamping them directly would be undone by the next `make emit`.
// tools/manifest.yaml is their source and is stamped instead.
//
// Idempotent, and a pure function of its argument — the release workflow runs it
// again on a tree that should already be stamped, so a correct release makes it
// a verified no-op rather than a step anyone has to trust.

const { readFileSync, writeFileSync } = require("node:fs");
const { join } = require("node:path");

const ROOT = join(__dirname, "..");

function main() {
  const version = process.argv[2];
  if (!version || !/^\d+\.\d+\.\d+(-[\w.]+)?$/.test(version)) {
    console.error("usage: stamp-version.js <semver>  (e.g. 1.4.0)");
    process.exit(2);
  }

  stampManifest(version);
  stampInstaller(version);
  stampJson(join(ROOT, "server.json"), (doc) => {
    doc.version = version;
    for (const pkg of doc.packages ?? []) {
      pkg.version = version;

      // An OCI package carries the version twice: once in `version`, and again
      // as the tag inside `identifier`. The MCP registry rejects an OCI entry
      // with a registryBaseUrl and wants that canonical reference instead, so
      // the tag is not optional — and stamping only `version` would leave the
      // registry advertising this release while pointing at the previous
      // release's image.
      if (pkg.registryType === "oci") {
        pkg.identifier = `${pkg.identifier.split(":")[0]}:${version}`;
      }
    }
  });
  console.log(`stamped ${version} — run \`make emit\` to regenerate the plugin manifests`);
}

// A line edit rather than a YAML round-trip: tools/manifest.yaml is the
// authoring source and carries comments explaining every field, and no YAML
// library preserves them. The anchored pattern matches the one column-0
// `version:` line; nested keys under `server:` are indented and cannot match.
function stampManifest(version) {
  const path = join(ROOT, "tools", "manifest.yaml");
  const before = readFileSync(path, "utf8");
  const after = before.replace(/^version: .*$/m, `version: ${version}`);
  if (after === before && !before.includes(`version: ${version}`)) {
    console.error(`${path} has no top-level version: line to stamp`);
    process.exit(1);
  }
  writeFileSync(path, after);
}

// Versions only. The platform pins are deliberately not written here: this runs
// on the release commit, and a committed pin can only name a version that does
// not exist yet, which npm records in the lockfile as the placeholder
// `{"optional": true}`. That placeholder is accepted right up until the version
// it stands for is published, at which point `npm ci` refuses the lockfile as
// out of sync — turning every push between one release and the next red, which
// is what happened after 0.1.0 shipped.
//
// scripts/build-platform-packages.js writes the pins instead, immediately after
// building the packages they name and immediately before publish.
function stampInstaller(version) {
  stampJson(join(ROOT, "installer", "package.json"), (doc) => {
    doc.version = version;
  });

  // The lockfile carries the version twice and is never published; `npm ci`
  // reads it, so it has to agree with package.json about which release this is.
  stampJson(join(ROOT, "installer", "package-lock.json"), (doc) => {
    doc.version = version;
    const root = doc.packages?.[""];
    if (!root) {
      console.error("installer/package-lock.json has no root package entry");
      process.exit(1);
    }
    root.version = version;
  });
}

function stampJson(path, mutate) {
  const doc = JSON.parse(readFileSync(path, "utf8"));
  mutate(doc);
  writeFileSync(path, `${JSON.stringify(doc, null, 2)}\n`);
}

main();

#!/usr/bin/env node
"use strict";

// Fallback binary download.
//
// The primary delivery path is optionalDependencies: npm, pnpm and yarn each
// install only the platform package matching the host, and that path works even
// when install scripts are blocked — which pnpm v10 does by default. This script
// exists for the cases where that did not happen, and it is deliberately
// best-effort: a failure here must not fail the install, because the wrapper
// prints a precise remediation the first time it cannot find the binary.
//
// What is not optional is verification. The download is checked against the
// checksums.txt published with the release before it is made executable; an
// asset with no published checksum is refused rather than trusted.

const { chmodSync, mkdirSync, renameSync, writeFileSync } = require("node:fs");
const { join } = require("node:path");

const REPO = "noetive/noetive-mcp";

function main() {
  let resolveBinary;
  let verifyDownload;
  let binaryName;
  let hostKey;
  try {
    ({ resolveBinary, binaryName, hostKey } = require("./dist/src/resolveBinary.js"));
    ({ verifyDownload } = require("./dist/src/verify.js"));
  } catch {
    // Installed from a tarball built without dist/, or mid-build in the repo.
    return;
  }

  try {
    resolveBinary();
    return; // The optional dependency landed; nothing to do.
  } catch {
    // Fall through to the download.
  }

  download(binaryName(), hostKey(), verifyDownload).catch((err) => {
    console.error(`noetive-mcp: could not pre-fetch the binary (${err.message}).`);
    console.error(`noetive-mcp: it will be resolved on first run, or install it with \`npm rebuild @noetive/mcp-server\`.`);
  });
}

async function download(executable, host, verifyDownload) {
  const version = require("./package.json").version;
  const asset = `noetive-mcp-${host}${executable.endsWith(".exe") ? ".exe" : ""}`;
  const base = `https://github.com/${REPO}/releases/download/v${version}`;

  const checksums = await fetchText(`${base}/checksums.txt`);

  const response = await fetch(`${base}/${asset}`, { redirect: "follow" });
  if (!response.ok) throw new Error(`GET ${asset} returned ${response.status}`);
  const bytes = Buffer.from(await response.arrayBuffer());

  const dir = join(__dirname, "bin");
  mkdirSync(dir, { recursive: true });

  // Verify before the file is given its final name, so a mismatched download is
  // never even momentarily present at the path the wrapper executes.
  const staged = join(dir, `.${asset}.partial`);
  writeFileSync(staged, bytes);
  verifyDownload(staged, asset, checksums);

  const target = join(dir, executable);
  renameSync(staged, target);
  chmodSync(target, 0o755);
}

async function fetchText(url) {
  const response = await fetch(url, { redirect: "follow" });
  if (!response.ok) throw new Error(`GET ${url} returned ${response.status}`);
  return response.text();
}

main();
